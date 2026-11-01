# Paper candidates: an independent top 20

A ranked list from a research pass on 2026-11-01. It was built to help pick
the final 10, without any swap suggestions. Selection criteria:
- seminal in systems engineering and distributed systems
- possible to experiment with in Go on a laptop
- has a measurable claim
- has a good angle for property-based or fuzz testing
- not primarily about hashing

Two hash-adjacent papers are included, with a reason given for each. The
URLs were checked for an open PDF on that date unless marked otherwise.

**Overlap** compares each entry with the current list in the root
`README.md`.

| # | Paper | Year | Diff | Go experiment, and the claim it measures | Property / fuzz angle | Overlap |
|---|-------|------|------|------------------------------------------|-----------------------|---------|
| 1 | **Time, Clocks, and the Ordering of Events**, Lamport (CACM). [pdf](https://lamport.azurewebsites.net/pubs/time-clocks.pdf); vector clocks: [Mattern 1988](https://www.vs.inf.ethz.ch/publ/papers/VirtTimeGlobStates.pdf) | 1978 | 2 | A deterministic network simulator with Lamport clocks and vector clocks. **Claim:** Lamport clocks are consistent with happens-before but don't characterize it; vector clocks do. Measure how often Lamport clocks falsely order concurrent events | Fuzz random traces and check both implications against the true happens-before; the paper's mutex algorithm never admits two processes at once | none (a prerequisite for Chandy–Lamport) |
| 2 | **Raft**, Ongaro & Ousterhout (USENIX ATC). [pdf](https://raft.github.io/raft.pdf) | 2014 | 5 | Leader election and log replication over the simulator with drops, partitions, and crashes, plus a KV store on top. **Claim:** the safety properties hold under every fault, and randomized timeouts elect a leader quickly | Fuzz the fault schedule and check the safety invariants after every step; check the KV history for linearizability | none |
| 3 | **Distributed Snapshots**, Chandy & Lamport (ACM TOCS). [pdf](https://lamport.azurewebsites.net/pubs/chandy.pdf) | 1985 | 3 | The marker algorithm, with a conserved quantity such as money moving between processes. **Claim:** a snapshot is consistent and includes in-flight messages | The conserved total always matches; no message is recorded twice; concurrent snapshots are still consistent | **Same as your #7** |
| 4 | **SWIM**, Das, Gupta & Motivala (DSN). [pdf](https://www.cs.cornell.edu/projects/Quicksilver/public_pdfs/SWIM.pdf) | 2002 | 3 | Ping, indirect ping, and suspicion, with membership updates piggybacked. **Claim:** load per member stays constant as the group grows; indirect pings cut false positives; updates spread in O(log N) rounds | No member suspects itself; incarnation numbers only increase; membership views converge | none |
| 5 | **The LSM-Tree**, O'Neil et al. (Acta Informatica). [pdf (authors)](https://www.cs.umb.edu/~poneil/lsmtree.pdf) | 1996 | 4 | Memtable, sorted files with Bloom filters, compaction. **Claim:** better write throughput, at the cost of write and read amplification | Model-based fuzz against a map; the newest version always wins; files within a level don't overlap | **Same as your #5** (also covers Bloom filters) |
| 6 | **CRDTs**, Shapiro et al. (SSS). [HAL](https://inria.hal.science/hal-00932836/document) | 2011 | 3 | G-counter, PN-counter, LWW register, OR-set, under arbitrary delivery order. **Claim:** strong eventual consistency without coordination | Merge is commutative, associative, and idempotent; replicas converge under any delivery schedule | none |
| 7 | **Michael–Scott concurrent queues** (PODC). [pdf](https://www.cs.rochester.edu/u/scott/papers/1996_PODC_queues.pdf) | 1996 | 3 | A lock-free queue, the paper's two-lock queue, a mutex queue, and a channel. **Claim:** the non-blocking queue scales under contention | Whatever goes in comes out; per-producer FIFO order is kept; run under `-race` and a linearizability check | none |
| 8 | **Dynamo**, DeCandia et al. (SOSP). [pdf](https://www.allthingsdistributed.com/files/amazon-dynamo-sosp2007.pdf) | 2007 | 4 | N/R/W quorums, vector clocks, hinted handoff, read repair; jumphash can do the placement. **Claim:** R+W>N means no stale reads; smaller quorums trade consistency for latency | A read dominates every acknowledged write; vector clocks never go backwards; hinted writes are eventually delivered | adjacent to #3 and #6 (it uses a ring) |
| 9 | **Epidemic Algorithms**, Demers et al. (PODC). [pdf](http://bitsavers.org/pdf/xerox/parc/techReports/CSL-89-1_Epidemic_Algorithms_for_Replicated_Database_Maintenance.pdf) | 1987 | 3 | Anti-entropy and rumor mongering. **Claim:** convergence in O(log N) rounds; rumor mongering leaves a predictable residue of nodes that never hear the update | Every schedule converges; the residue stays within the paper's statistical bounds | none |
| 10 | **Linearizability**, Herlihy & Wing (TOPLAS). [pdf](https://cs.brown.edu/~mph/HerlihyW90/p463-herlihy.pdf) | 1990 | 4 | A linearizability checker, compared with [Porcupine](https://github.com/anishathalye/porcupine). **Claim:** linearizability is compositional; checking cost grows exponentially with concurrency | The checker accepts every correct history and rejects every injected bug | none |
| 11 | **Congestion Avoidance and Control**, Jacobson (SIGCOMM). [pdf](https://ee.lbl.gov/papers/congavoid.pdf) | 1988 | 4 | Senders sharing a bottleneck link, with slow start, AIMD, and the paper's retransmit-timer estimator. **Claim:** fair, efficient sharing with no congestion collapse | The window halves on loss; throughput never collapses under fuzzed loss and delay | none (networking) |
| 12 | **The Tail at Scale**, Dean & Barroso (CACM). [Google Research](https://research.google/pubs/pub40801/) | 2013 | 2 | Requests fanned out to many backends, plus hedged and tied requests. **Claim:** fan-out amplifies tail latency; hedging cuts p99.9 at about 5% extra load | P(slow) = 1−(1−p)^M; tied requests complete exactly once | none |
| 13 | **Reed–Solomon tutorial**, Plank (SP&E). [pdf](https://web.eecs.utk.edu/~jplank/plank/papers/CS-96-332.pdf), plus [correction](https://web.eecs.utk.edu/~jplank/plank/papers/CS-03-504.pdf) | 1997 | 4 | Arithmetic over GF(2^8), 4+2 encode, reconstruct after erasures. **Claim:** any k of the k+m shards rebuild the data, at (k+m)/k storage overhead | decode(erase(encode(d))) == d; m+1 erasures are reported as unrecoverable | **Same topic as your #1** (Plank is the paper you'd implement from; 1960 is the theory) |
| 14 | **Consistent Hashing**, Karger et al. (STOC). Hash-adjacent; included because it's a placement result and already fixed on your list | 1997 | 2 | A ring with virtual nodes, compared with jumphash. **Claim:** an add moves only about 1/n of the keys; virtual nodes improve balance | Moved keys only go to the new node; load skew stays within bounds | **Same as your #3**; overlaps #6 |
| 15 | **HyperLogLog**, Flajolet et al. (AofA). [pdf](https://algo.inria.fr/flajolet/Publications/FlFuGaMe07.pdf). Hash-adjacent: the hash only supplies randomness; it's a sketch paper | 2007 | 3 | Stream millions of IDs and compare with an exact map. **Claim:** error ≈ 1.04/√m | merge(sketch A, sketch B) == sketch(A ∪ B); error stays within bounds | **Same as your #10** |
| 16 | **Skip Lists**, Pugh (CACM). [pdf](https://15721.courses.cs.cmu.edu/spring2018/papers/08-oltpindexes1/pugh-skiplists-cacm1990.pdf) | 1990 | 2 | A generic skip list, usable as the LSM memtable. **Claim:** expected O(log n) search; p = 1/4 saves memory | Model-based fuzz against a sorted map; each level is a subsequence of the one below | none (feeds #5) |
| 17 | **MapReduce**, Dean & Ghemawat (OSDI). [pdf](https://static.googleusercontent.com/media/research.google.com/en//archive/mapreduce-osdi04.pdf) | 2004 | 3 | A coordinator and workers, with re-execution and backup tasks. **Claim:** backup tasks cut job time a lot (the paper reports 44% slower without them) | Output under failures equals a sequential reference; no task is committed twice | none |
| 18 | **ARC**, Megiddo & Modha (FAST). [pdf](https://www.usenix.org/legacy/events/fast03/tech/full_papers/megiddo/megiddo.pdf) | 2003 | 3 | LRU, LFU, 2Q, and ARC on Zipf, scan, and loop workloads. **Claim:** ARC matches or beats LRU without tuning, and a scan can't flush its working set | List-size invariants; never much worse than the better of LRU and LFU | none (caching) |
| 19 | **The Byzantine Generals Problem**, Lamport, Shostak & Pease (TOPLAS). [pdf](https://lamport.azurewebsites.net/pubs/byz.pdf) | 1982 | 3 | OM(m), with traitors that follow pluggable strategies. **Claim:** agreement holds if and only if n ≥ 3m+1 | The fuzzer rediscovers the counterexample at n = 3m | none |
| 20 | **LBFS**, Muthitacharoen, Chen & Mazières (SOSP). [pdf](https://pdos.csail.mit.edu/papers/lbfs:sosp01/lbfs.pdf). It uses a rolling hash, but it's a storage and dedup paper | 2001 | 3 | Fixed-size chunking vs Rabin content-defined chunking after edits. **Claim:** only the chunks around an edit change | Boundaries away from an edit are stable; chunk sizes stay within min/max; joining the chunks gives back the input | **Same experiment as your #2** (LBFS states the claim; Rabin 1981 is the math) |

## Your current 10 against this list

| Yours | In the top 20? |
|-------|----------------|
| 1 Reed–Solomon 1960 | The topic is (#13), through Plank's tutorial |
| 2 Rabin 1981 | The topic is (#20), through LBFS |
| 3 Consistent hashing 1997 | Yes (#14) |
| 4 Bloom filters 1970 | No. Covered as part of the LSM experiment |
| 5 LSM-tree 1996 | Yes (#5) |
| 6 Jump hash 2014 | No. Already done; overlaps #14 |
| 7 Chandy–Lamport 1985 | Yes (#3) |
| 8 Cuckoo hashing | No. A hashing paper |
| 9 Burrows–Wheeler 1994 | No. Compression is outside the systems focus |
| 10 HyperLogLog 2007 | Yes (#15) |

**Not on your list at all:**
- consensus and replication: #2, #8
- failure detection and membership: #4, #9
- concurrency: #7, #10
- networking: #11
- caching: #18
- latency: #12

**Just missing the 20:**
- [Paxos Made Simple](https://lamport.azurewebsites.net/pubs/paxos-simple.pdf)
- [Hybrid Logical Clocks](https://cse.buffalo.edu/tech-reports/2014-04.pdf)
- the Phi accrual failure detector
- [MCS locks](https://www.cs.rochester.edu/u/scott/papers/1991_TOCS_synch.pdf)
- [Lamport's bakery](https://lamport.azurewebsites.net/pubs/bakery.pdf)
- [Dominant Resource Fairness](https://www.usenix.org/legacy/event/nsdi11/tech/full_papers/Ghodsi.pdf)
- [Lottery Scheduling](https://www.usenix.org/legacy/publications/library/proceedings/osdi/full_papers/waldspurger.pdf)
- [Consensus on Transaction Commit](https://lamport.azurewebsites.net/video/consensus-on-transaction-commit.pdf)
- [Dijkstra's self-stabilization](https://www.cs.utexas.edu/users/EWD/ewd04xx/EWD426.PDF)

## A suggested reading order, so tools and ideas carry over

- **Storage and data structures:** Skip lists → LSM → ARC → erasure coding
  → LBFS → HyperLogLog
- **Concurrency:** Michael–Scott queue → Linearizability (the checker tests
  the queue)
- **Distributed** (build one deterministic network simulator and reuse
  it): Lamport clocks → Chandy–Lamport → Epidemic → SWIM → CRDTs →
  Consistent hashing → Dynamo → Tail at Scale → Jacobson → Byzantine
  Generals → Raft → MapReduce
