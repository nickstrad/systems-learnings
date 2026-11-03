-- The experiments' blog tables (DESIGN.md §8). Run from lsmtree/:
--
--   duckdb -c '.read scripts/experiments.sql'
--
-- `make experiments` (here) and `make experiments-lsmtree` (at the
-- repository root) run it after each run, through the shared runner
-- ../shared/scripts/experiments.sh. It reads the files of all runs under
-- results/ by glob. The shared prelude it loads first (see its header)
-- checks every table below has a run, prints the runs, and defines
-- latest('<table>'): the rows of the newest run that wrote that table.
-- So a run of some experiments only (RUN=E3) shows its new tables beside
-- the others' last results. The last query compares runs.

SET VARIABLE tables = ['e1_batch_merge', 'e2_write_amp', 'e2_totals', 'e3_theorem31',
                        'e4_read_amp'];
.read ../shared/scripts/experiments_prelude.sql
.maxrows 100

-- Each experiment's rows from its latest run.
CREATE TEMP VIEW e1 AS FROM latest('e1_batch_merge');
CREATE TEMP VIEW e2_levels AS FROM latest('e2_write_amp');
CREATE TEMP VIEW e2_totals AS FROM latest('e2_totals');
CREATE TEMP VIEW e3 AS FROM latest('e3_theorem31');
CREATE TEMP VIEW e4 AS FROM latest('e4_read_amp');

-- Each experiment's events from the run its tables come from. They are
-- read once, into a table: the event logs are the largest files, so only
-- the runs named in latest_runs are read, and only their own experiment's
-- events kept.
SET VARIABLE event_files = (
    SELECT list(DISTINCT 'results/' || run_id || '/events.jsonl') FROM latest_runs);
CREATE TEMP TABLE events AS
    SELECT * FROM read_json_auto(getvariable('event_files'))
    WHERE (experiment, run_id) IN (SELECT (experiment, run_id) FROM latest_runs);

.print
.print == E1. Batch-merge parameter M: measured vs formula 3.2, M = (Sp/Se)·S0/(S0+S1)
.print    One C0→C1 merge per row, L1 fixed at n1 entries. Block I/O per insert vs formula 3.3, 2/M.
SELECT s1_over_s0 AS "S1/S0", n0, n1,
       round(measured_m, 2) AS measured_m,
       round(predicted_m, 2) AS predicted_m,
       round(m_ratio, 3) AS ratio,
       round(measured_block_io_per_insert, 3) AS io_per_insert,
       round(predicted_block_io_per_insert, 3) AS predicted_2_over_m
FROM e1
ORDER BY s1_over_s0 DESC;

.print
.print == E2. Write amplification by size ratio r and level (table bytes written per user byte)
.print    L0 is the flush. Random keys first, then sequential keys as the contrast.
PIVOT (
    SELECT pattern, r, 'L' || level AS level, round(write_amp, 2) AS write_amp
    FROM e2_levels
)
ON level IN ('L0', 'L1', 'L2', 'L3', 'L4', 'L5', 'L6')
USING first(write_amp)
ORDER BY pattern, r;

.print
.print == E2. Totals: table, WAL, and manifest bytes per user byte; model = formula 3.7 terms, all levels full
SELECT pattern, r, levels_used,
       round(table_write_amp, 2) AS table_amp,
       round(wal_write_amp, 2) AS wal_amp,
       round(manifest_write_amp, 2) AS manifest_amp,
       round(disk_write_amp, 2) AS disk_amp,
       round(model_write_amp, 2) AS model_table_amp
FROM e2_totals
ORDER BY pattern, r;

.print
.print == E3. Theorem 3.1: merge I/O per byte inserted as S1 varies, S0 and S2 fixed
.print    Predicted is formula 3.7, (2·r1+2)+(2·r2+1). Theory's minimum is at S1 = √(S0·S2).
SELECT s1_target // 1024 AS s1_kib,
       round(s1_over_geo_mean, 2) AS "S1/√(S0·S2)",
       round(r1, 1) AS r1, round(r2, 1) AS r2,
       round(measured_merge_io, 1) AS measured,
       round(predicted_merge_io, 1) AS predicted,
       bar(measured_merge_io, 0, (SELECT max(measured_merge_io) FROM e3), 30) AS curve,
       concat_ws(' ',
           CASE WHEN measured_merge_io = (SELECT min(measured_merge_io) FROM e3) THEN '<- measured min' END,
           CASE WHEN abs(ln(s1_over_geo_mean)) = (SELECT min(abs(ln(s1_over_geo_mean))) FROM e3)
                THEN '<- nearest √(S0·S2) = ' || round(geo_mean_s1 / 1024) || ' KiB' END) AS marker
FROM e3
ORDER BY s1_target;

.print
.print == E4. Read amplification: tables probed and data blocks read per Get, by Bloom filter setting
.print    Predicted: absent = p·tables, present = 1 + p·(tables-1), with p = (1-e^(-k/b))^k.
SELECT CASE WHEN bloom_bits_per_key = 0 THEN 'off' ELSE bloom_bits_per_key || ' bits/key' END AS bloom,
       probes_k AS k, lookup,
       round(tables_probed_per_get, 2) AS tables_per_get,
       round(blocks_per_get, 4) AS blocks_per_get,
       round(predicted_blocks_per_get, 4) AS predicted_blocks,
       round(measured_fp_rate, 5) AS fp_rate,
       round(predicted_fp_rate, 5) AS predicted_fp
FROM e4
ORDER BY bloom_bits_per_key, lookup DESC;

.print
.print == Event log: compactions per experiment and level (each experiment's latest run, all phases)
.print    M per merge = new_entries / output_blocks; tombstones are dropped only by merges into the bottom.
SELECT experiment, from_level || '->' || to_level AS merge,
       count(*) AS compactions,
       round(avg(bytes_written) / 1024, 1) AS avg_kib_written,
       round(sum(bytes_written) / 1048576, 1) AS total_mib_written,
       round(sum(bytes_read) / 1048576, 1) AS total_mib_read,
       round(sum(new_entries) / sum(output_blocks), 2) AS m,
       sum(tombstones_dropped) AS tombstones_dropped
FROM events
WHERE kind = 'compaction'
GROUP BY experiment, from_level, to_level
ORDER BY experiment, from_level;

.print
.print == Event log: flushes and compactions per configuration (each experiment's latest run)
SELECT experiment, config,
       count(*) FILTER (WHERE kind = 'flush') AS flushes,
       count(*) FILTER (WHERE kind = 'compaction') AS compactions,
       round(sum(bytes_written) FILTER (WHERE kind = 'flush') / 1048576, 2) AS flush_mib,
       round(sum(bytes_written) FILTER (WHERE kind = 'compaction') / 1048576, 2) AS compaction_mib
FROM events
GROUP BY experiment, config
ORDER BY experiment, regexp_replace(config, '[0-9]+', ''), regexp_extract(config, '[0-9]+')::INTEGER;

.print
.print == Across runs: the headline number of each experiment, to spot regressions
SELECT run.run_id, run.git_commit,
       (SELECT round(avg(m_ratio), 3) FROM runs_of('e1_batch_merge') e WHERE e.run_id = run.run_id) AS e1_mean_m_ratio,
       (SELECT round(table_write_amp, 2) FROM runs_of('e2_totals') e
         WHERE e.run_id = run.run_id AND e.pattern = 'random' AND e.r = 10) AS e2_amp_random_r10,
       (SELECT s1_target // 1024 FROM runs_of('e3_theorem31') e
         WHERE e.run_id = run.run_id ORDER BY measured_merge_io LIMIT 1) AS e3_best_s1_kib,
       (SELECT round(blocks_per_get, 4) FROM runs_of('e4_read_amp') e
         WHERE e.run_id = run.run_id AND e.bloom_bits_per_key = 10 AND e.lookup = 'absent') AS e4_absent_blocks_10bits
FROM runs run
ORDER BY run.run_id;
