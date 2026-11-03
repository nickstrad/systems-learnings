// Package wal is the write-ahead log: the paper's recovery log (§4.2).
//
// Every write is appended to the log before it goes into the memtable, so
// a crash loses nothing that was synced. There is one log file per
// memtable; once the memtable is flushed to an SSTable and the manifest
// says so, its log is deleted.
//
// # Record format
//
// A log is a sequence of records, each:
//
//	crc u32 | len u32 | payload
//
// The payload is one entry in the shared entry encoding (package codec):
//
//	keylen uvarint | key | seq u64 | kind u8 | vallen uvarint | val
//
// Integers are little-endian. len is the byte length of the payload. crc
// is the CRC32-C (Castagnoli) of len and the payload: everything after
// the crc field.
//
// # Torn tails
//
// A crash can leave the last record half-written. Replay stops at the
// first record that is short, fails its CRC, or does not decode, and
// reports how many bytes it dropped. It cannot tell a torn tail from a
// record damaged in the middle of the file (bit rot): both look like a
// bad record. So everything from the first bad record on is dropped,
// even if good records follow it. That keeps the recovered writes a
// prefix of the acknowledged ones, which is what the store relies on: a
// later write is never recovered while an earlier one is lost.
package wal

// headerSize is the record header: crc u32 + len u32.
const headerSize = 8
