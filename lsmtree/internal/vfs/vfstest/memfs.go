// Package vfstest holds test helpers for package vfs: MemFS, an in-memory
// filesystem that can inject faults and simulate crashes, and FSContract,
// the test suite every vfs.FS implementation must pass.
package vfstest

import (
	"io"
	"io/fs"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"sync"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
	"github.com/nickstrad/systems-learnings/shared/assert"
)

// Op names a filesystem operation, for fault injection.
type Op int

const (
	OpCreate Op = iota
	OpOpen
	OpRead
	OpWrite
	OpSync
	OpRename
	OpRemove
	OpList
	OpMkdir
	OpSyncDir
	OpLock
	OpStat // File.Size
)

var opNames = [...]string{"create", "open", "read", "write", "sync", "rename", "remove", "list", "mkdir", "syncdir", "lock", "stat"}

func (o Op) String() string { return opNames[o] }

// Fault decides whether an operation fails. A nil error lets it run; a
// non-nil error makes it fail with no effect. It is called with MemFS's
// lock held, so it must not call back into the MemFS.
type Fault func(op Op, path string) error

// FailNth returns a Fault that fails the nth call to op (counting from 1,
// across all paths) with err, and lets every other call through.
func FailNth(op Op, n int, err error) Fault {
	seen := 0
	return func(o Op, _ string) error {
		if o != op {
			return nil
		}
		seen++
		if seen == n {
			return err
		}
		return nil
	}
}

// inode is a file's contents. Two names can point at one inode while a
// rename is not yet durable: the old name in the durable namespace, the
// new one in the live namespace.
//
// Invariant: 0 <= synced <= len(data). Files are append-only, so the
// durable bytes are always a prefix of the current bytes.
type inode struct {
	data   []byte
	synced int // length of the prefix that survives a crash
}

// nsOp is a namespace change not yet made durable by SyncDir.
type nsOp struct {
	kind Op // OpCreate, OpRename, or OpRemove
	name string
	from string // OpRename only
	node *inode // OpCreate only
}

// touches reports whether SyncDir(dir) makes op durable.
func (op nsOp) touches(dir string) bool {
	return filepath.Dir(op.name) == dir || (op.kind == OpRename && filepath.Dir(op.from) == dir)
}

// MemFS is an in-memory vfs.FS for tests. It follows the durability model
// in package vfs, with namespace changes kept in order:
//
//   - File data survives Crash only up to the last Sync.
//   - Creates, renames, and removes survive only once SyncDir has run on
//     their directory. Pending namespace changes are kept in order, and
//     SyncDir makes durable every change up to the last one touching that
//     directory, like an ordered metadata journal.
//   - Directories made by MkdirAll are durable at once (a simplification:
//     the store creates its directory once, at open).
//
// It is safe for concurrent use.
type MemFS struct {
	mu      sync.Mutex
	files   map[string]*inode // the namespace as the running program sees it
	durable map[string]*inode // the namespace a crash would leave
	pending []nsOp            // namespace changes since the last SyncDir, in order
	dirs    map[string]bool
	locks   map[string]*memLock
	gen     int // bumped by each crash, to invalidate open files
	fault   Fault
}

var _ vfs.FS = (*MemFS)(nil)

// NewMemFS returns an empty MemFS containing only the root directory "/"
// (and ".", the parent of relative names).
func NewMemFS() *MemFS {
	return &MemFS{
		files:   map[string]*inode{},
		durable: map[string]*inode{},
		dirs:    map[string]bool{"/": true, ".": true},
		locks:   map[string]*memLock{},
	}
}

// SetFault installs f, replacing any earlier fault. nil removes it.
func (m *MemFS) SetFault(f Fault) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fault = f
}

// Crash simulates power loss: every file drops its unsynced bytes, every
// namespace change not made durable by SyncDir is undone, every lock is
// released, and every open file stops working.
func (m *MemFS) Crash() {
	m.crash(func(int) int { return 0 })
}

// CrashTorn is a harsher crash: besides the durable state, each file keeps
// a random prefix of its unsynced bytes (a torn write), and a random prefix
// of the pending namespace changes survives. This is what a real disk may
// do, and it is what WAL recovery must handle.
func (m *MemFS) CrashTorn(rng *rand.Rand) {
	m.crash(func(n int) int { return rng.IntN(n + 1) })
}

// crash keeps pick(len(pending)) of the pending namespace changes, then
// trims each surviving file to its synced length plus pick(unsynced)
// bytes. pick returns a value in [0, n].
func (m *MemFS) crash(pick func(n int) int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, op := range m.pending[:pick(len(m.pending))] {
		m.applyDurable(op)
	}
	m.pending = nil
	// Each durable name has its own inode: a create makes a fresh one and
	// a rename moves it, so no inode is trimmed twice.
	m.files = map[string]*inode{}
	for name, n := range m.durable {
		m.files[name] = n
		n.data = n.data[:n.synced+pick(len(n.data)-n.synced)]
		n.synced = len(n.data)
	}
	m.locks = map[string]*memLock{}
	m.gen++
	m.checkInvariant()
}

func (m *MemFS) applyDurable(op nsOp) {
	switch op.kind {
	case OpCreate:
		m.durable[op.name] = op.node
	case OpRename:
		if n, ok := m.durable[op.from]; ok {
			m.durable[op.name] = n
			delete(m.durable, op.from)
		}
	case OpRemove:
		delete(m.durable, op.name)
	}
}

// before runs the fault hook. Callers hold m.mu.
func (m *MemFS) before(op Op, name string) error {
	if m.fault == nil {
		return nil
	}
	if err := m.fault(op, name); err != nil {
		return pathErr(op, name, err)
	}
	return nil
}

func pathErr(op Op, name string, err error) error {
	return &fs.PathError{Op: op.String(), Path: name, Err: err}
}

// create makes name a new empty file. Callers hold m.mu and have checked
// that the parent directory exists.
func (m *MemFS) create(name string) *inode {
	n := &inode{}
	m.files[name] = n
	m.pending = append(m.pending, nsOp{kind: OpCreate, name: name, node: n})
	return n
}

// Create implements vfs.FS.
func (m *MemFS) Create(name string) (vfs.WritableFile, error) {
	name = filepath.Clean(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.before(OpCreate, name); err != nil {
		return nil, err
	}
	if !m.dirs[filepath.Dir(name)] || m.dirs[name] {
		return nil, pathErr(OpCreate, name, fs.ErrNotExist)
	}
	return &memFile{fs: m, node: m.create(name), name: name, gen: m.gen}, nil
}

// Open implements vfs.FS.
func (m *MemFS) Open(name string) (vfs.ReadableFile, error) {
	name = filepath.Clean(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.before(OpOpen, name); err != nil {
		return nil, err
	}
	n, ok := m.files[name]
	if !ok {
		return nil, pathErr(OpOpen, name, fs.ErrNotExist)
	}
	return &memFile{fs: m, node: n, name: name, gen: m.gen}, nil
}

// Rename implements vfs.FS.
func (m *MemFS) Rename(oldname, newname string) error {
	oldname, newname = filepath.Clean(oldname), filepath.Clean(newname)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.before(OpRename, oldname); err != nil {
		return err
	}
	n, ok := m.files[oldname]
	if !ok || !m.dirs[filepath.Dir(newname)] {
		return pathErr(OpRename, oldname, fs.ErrNotExist)
	}
	if oldname == newname {
		return nil
	}
	m.files[newname] = n
	delete(m.files, oldname)
	m.pending = append(m.pending, nsOp{kind: OpRename, name: newname, from: oldname})
	return nil
}

// Remove implements vfs.FS.
func (m *MemFS) Remove(name string) error {
	name = filepath.Clean(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.before(OpRemove, name); err != nil {
		return err
	}
	if _, ok := m.files[name]; !ok {
		return pathErr(OpRemove, name, fs.ErrNotExist)
	}
	delete(m.files, name)
	m.pending = append(m.pending, nsOp{kind: OpRemove, name: name})
	return nil
}

// List implements vfs.FS.
func (m *MemFS) List(dir string) ([]string, error) {
	dir = filepath.Clean(dir)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.before(OpList, dir); err != nil {
		return nil, err
	}
	if !m.dirs[dir] {
		return nil, pathErr(OpList, dir, fs.ErrNotExist)
	}
	var names []string
	for name := range m.files {
		if filepath.Dir(name) == dir {
			names = append(names, filepath.Base(name))
		}
	}
	slices.Sort(names)
	return names, nil
}

// MkdirAll implements vfs.FS.
func (m *MemFS) MkdirAll(dir string) error {
	dir = filepath.Clean(dir)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.before(OpMkdir, dir); err != nil {
		return err
	}
	for d := dir; !m.dirs[d]; d = filepath.Dir(d) {
		if _, isFile := m.files[d]; isFile {
			return pathErr(OpMkdir, d, fs.ErrExist)
		}
	}
	for d := dir; !m.dirs[d]; d = filepath.Dir(d) {
		m.dirs[d] = true
	}
	return nil
}

// SyncDir implements vfs.FS. It makes durable every pending namespace
// change up to and including the last one that touches dir.
func (m *MemFS) SyncDir(dir string) error {
	dir = filepath.Clean(dir)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.before(OpSyncDir, dir); err != nil {
		return err
	}
	if !m.dirs[dir] {
		return pathErr(OpSyncDir, dir, fs.ErrNotExist)
	}
	last := -1
	for i, op := range m.pending {
		if op.touches(dir) {
			last = i
		}
	}
	for _, op := range m.pending[:last+1] {
		m.applyDurable(op)
	}
	m.pending = slices.Delete(m.pending, 0, last+1)
	return nil
}

// Lock implements vfs.FS. Like osfs, it creates the lock file if it is
// missing, so List shows it. The lock itself lives only in memory, and a
// crash releases it.
func (m *MemFS) Lock(name string) (io.Closer, error) {
	name = filepath.Clean(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.before(OpLock, name); err != nil {
		return nil, err
	}
	if !m.dirs[filepath.Dir(name)] {
		return nil, pathErr(OpLock, name, fs.ErrNotExist)
	}
	if m.locks[name] != nil {
		return nil, pathErr(OpLock, name, vfs.ErrLocked)
	}
	if _, ok := m.files[name]; !ok {
		m.create(name)
	}
	l := &memLock{fs: m, name: name}
	m.locks[name] = l
	return l, nil
}

// checkInvariant asserts that every file's synced prefix is in range.
// Callers hold m.mu.
func (m *MemFS) checkInvariant() {
	if !assert.Enabled {
		return
	}
	for name, n := range m.files {
		assert.That(0 <= n.synced && n.synced <= len(n.data),
			"memfs: %s has synced=%d outside [0, %d]", name, n.synced, len(n.data))
	}
}

type memLock struct {
	fs     *MemFS
	name   string
	closed bool
}

func (l *memLock) Close() error {
	l.fs.mu.Lock()
	defer l.fs.mu.Unlock()
	if l.closed {
		return pathErr(OpLock, l.name, fs.ErrClosed)
	}
	l.closed = true
	if l.fs.locks[l.name] == l { // after a crash, the lock may belong to someone else
		delete(l.fs.locks, l.name)
	}
	return nil
}

// memFile is an open file on an inode. One type serves both kinds of
// file; the vfs interfaces it is returned as decide which methods a
// caller can reach.
type memFile struct {
	fs     *MemFS
	node   *inode
	name   string
	gen    int
	off    int64 // read offset, for Read
	closed bool
}

// check returns an error if the file is closed or from before a crash,
// then runs the fault hook. Callers hold f.fs.mu.
func (f *memFile) check(op Op) error {
	if f.closed || f.gen != f.fs.gen {
		return pathErr(op, f.name, fs.ErrClosed)
	}
	return f.fs.before(op, f.name)
}

func (f *memFile) Write(p []byte) (int, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if err := f.check(OpWrite); err != nil {
		return 0, err
	}
	f.node.data = append(f.node.data, p...)
	return len(p), nil
}

func (f *memFile) Sync() error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if err := f.check(OpSync); err != nil {
		return err
	}
	f.node.synced = len(f.node.data)
	return nil
}

func (f *memFile) Read(p []byte) (int, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if err := f.check(OpRead); err != nil {
		return 0, err
	}
	if f.off >= int64(len(f.node.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.node.data[f.off:])
	f.off += int64(n)
	return n, nil
}

func (f *memFile) ReadAt(p []byte, off int64) (int, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if err := f.check(OpRead); err != nil {
		return 0, err
	}
	if off < 0 {
		return 0, pathErr(OpRead, f.name, fs.ErrInvalid)
	}
	if off >= int64(len(f.node.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.node.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *memFile) Size() (int64, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if err := f.check(OpStat); err != nil {
		return 0, err
	}
	return int64(len(f.node.data)), nil
}

func (f *memFile) Close() error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if f.closed {
		return pathErr(OpOpen, f.name, fs.ErrClosed)
	}
	f.closed = true
	return nil
}
