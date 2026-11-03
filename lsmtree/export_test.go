package lsmtree

// CheckInvariant runs the DB's invariant check, and ForgetReader breaks
// it by closing and dropping a live table's reader, so a test can show
// the check trips.
func (db *DB) CheckInvariant() { db.checkInvariant() }

func (db *DB) ForgetReader(num uint64) {
	db.tables[num].Close()
	delete(db.tables, num)
}
