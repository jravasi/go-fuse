package zipfs

/*

This provides a practical example of mounting Go-fuse path filesystems
on top of each other.

It is a file system that configures a Zip filesystem at /zipmount when writing
path/to/zipfile to /config/zipmount

*/

import (
	"github.com/hanwen/go-fuse/fuse"
	"log"
	"os"
	"path/filepath"
	"sync"
	"strings"
)

var _ = log.Printf

const (
	CONFIG_PREFIX = "config/"
)

// zipCreateFile is a placeholder file to receive the write containing
// the path to the zip file.
type zipCreateFile struct {
	// Basename of the entry in the FS.
	Basename string
	zfs      *MultiZipFs

	fuse.DefaultFile
}

func (me *zipCreateFile) Write(input *fuse.WriteIn, nameBytes []byte) (uint32, fuse.Status) {
	if me.zfs == nil {
		// TODO
		return 0, fuse.EPERM
	}
	zipFile := string(nameBytes)

	zipFile = strings.Trim(zipFile, "\n ")
	fs, err := NewArchiveFileSystem(zipFile)
	if err != nil {
		// TODO
		log.Println("NewZipArchiveFileSystem failed.")
		me.zfs.pendingZips[me.Basename] = false, false
		return 0, fuse.ENOSYS
	}

	code := me.zfs.Connector.Mount("/"+filepath.Base(me.Basename), fs, nil)
	if code != fuse.OK {
		return 0, code

	}
	// TODO. locks?

	me.zfs.zips[me.Basename] = fs
	me.zfs.dirZipFileMap[me.Basename] = zipFile
	me.zfs.pendingZips[me.Basename] = false, false

	me.zfs = nil

	return uint32(len(nameBytes)), code
}

////////////////////////////////////////////////////////////////

// MultiZipFs is a path filesystem that mounts zipfiles.  It needs a
// reference to the FileSystemConnector to be able to execute
// mounts.
type MultiZipFs struct {
	Connector     *fuse.FileSystemConnector
	lock          sync.RWMutex
	zips          map[string]*MemTreeFileSystem
	pendingZips   map[string]bool
	dirZipFileMap map[string]string

	fuse.DefaultFileSystem
}

func NewMultiZipFs() *MultiZipFs {
	m := new(MultiZipFs)
	m.zips = make(map[string]*MemTreeFileSystem)
	m.pendingZips = make(map[string]bool)
	m.dirZipFileMap = make(map[string]string)
	return m
}

func (me *MultiZipFs) Mount(connector *fuse.FileSystemConnector) fuse.Status {
	me.Connector = connector
	return fuse.OK
}

func (me *MultiZipFs) OpenDir(name string) (stream chan fuse.DirEntry, code fuse.Status) {
	me.lock.RLock()
	defer me.lock.RUnlock()

	// We don't use a goroutine, since we don't want to hold the
	// lock.
	stream = make(chan fuse.DirEntry,
		len(me.pendingZips)+len(me.zips)+2)

	submode := uint32(fuse.S_IFDIR | 0700)
	if name == "config" {
		submode = fuse.S_IFREG | 0600
	}

	for k, _ := range me.zips {
		var d fuse.DirEntry
		d.Name = k
		d.Mode = submode
		stream <- fuse.DirEntry(d)
	}
	for k, _ := range me.pendingZips {
		var d fuse.DirEntry
		d.Name = k
		d.Mode = submode
		stream <- fuse.DirEntry(d)
	}

	if name == "" {
		var d fuse.DirEntry
		d.Name = "config"
		d.Mode = fuse.S_IFDIR | 0700
		stream <- fuse.DirEntry(d)
	}

	close(stream)
	return stream, fuse.OK
}

func (me *MultiZipFs) GetAttr(name string) (*os.FileInfo, fuse.Status) {
	a := &os.FileInfo{}
	if name == "" {
		// Should not write in top dir.
		a.Mode = fuse.S_IFDIR | 0500
		return a, fuse.OK
	}

	if name == "config" {
		// TODO
		a.Mode = fuse.S_IFDIR | 0700
		return a, fuse.OK
	}

	dir, base := filepath.Split(name)
	if dir != "" && dir != CONFIG_PREFIX {
		return nil, fuse.ENOENT
	}
	submode := uint32(fuse.S_IFDIR | 0700)
	if dir == CONFIG_PREFIX {
		submode = fuse.S_IFREG | 0600
	}

	me.lock.RLock()
	defer me.lock.RUnlock()

	a.Mode = submode
	_, hasDir := me.zips[base]
	if hasDir {
		return a, fuse.OK
	}
	_, hasDir = me.pendingZips[base]
	if hasDir {
		return a, fuse.OK
	}

	return nil, fuse.ENOENT
}

func (me *MultiZipFs) Unlink(name string) (code fuse.Status) {
	dir, basename := filepath.Split(name)
	if dir == CONFIG_PREFIX {
		me.lock.Lock()
		defer me.lock.Unlock()

		_, ok := me.zips[basename]
		if ok {
			me.zips[basename] = nil, false
			me.dirZipFileMap[basename] = "", false
			return fuse.OK
		} else {
			return fuse.ENOENT
		}
	}
	return fuse.EPERM
}

func (me *MultiZipFs) Open(name string, flags uint32) (file fuse.File, code fuse.Status) {
	if 0 != flags&uint32(fuse.O_ANYWRITE) {
		return nil, fuse.EPERM
	}

	dir, basename := filepath.Split(name)
	if dir == CONFIG_PREFIX {
		me.lock.RLock()
		defer me.lock.RUnlock()

		orig, ok := me.dirZipFileMap[basename]
		if !ok {
			return nil, fuse.ENOENT
		}

		return fuse.NewReadOnlyFile([]byte(orig)), fuse.OK
	}

	return nil, fuse.ENOENT
}

func (me *MultiZipFs) Create(name string, flags uint32, mode uint32) (file fuse.File, code fuse.Status) {
	dir, base := filepath.Split(name)
	if dir != CONFIG_PREFIX {
		return nil, fuse.EPERM
	}

	z := new(zipCreateFile)
	z.Basename = base
	z.zfs = me

	me.lock.Lock()
	defer me.lock.Unlock()

	me.pendingZips[z.Basename] = true

	return z, fuse.OK
}
