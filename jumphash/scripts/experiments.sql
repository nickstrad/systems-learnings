-- The experiments' blog tables (H1-H3; see README.md, "Experiments").
-- Run from jumphash/:
--
--   duckdb -c '.read scripts/experiments.sql'
--
-- `make experiments` (here) and `make experiments-jumphash` (at the
-- repository root) run it after each run, through the shared runner
-- ../shared/scripts/experiments.sh. It reads the files of all runs under
-- results/ by glob. The shared prelude it loads first (see its header)
-- checks every table below has a run, prints the runs, and defines
-- latest('<table>'): the rows of the newest run that wrote that table.
-- So a run of some experiments only (RUN=H1) shows its new tables beside
-- the others' last results. The last query compares runs.

SET VARIABLE tables = ['h1_movement', 'h2_balance', 'h3_jumps', 'h3_fit'];
.read ../shared/scripts/experiments_prelude.sql
.maxrows 200

-- Each experiment's rows from its latest run.
CREATE TEMP VIEW h1 AS FROM latest('h1_movement');
CREATE TEMP VIEW h2 AS FROM latest('h2_balance');
CREATE TEMP VIEW h3 AS FROM latest('h3_jumps');
CREATE TEMP VIEW h3_fit AS FROM latest('h3_fit');

.print
.print == H1. Keys moved when n buckets grow to n+1 (100,000 keys): jump vs modulo vs a Karger ring
.print    Minimal is 1/(n+1). Jump should sit on it; modulo moves n/(n+1); the ring is right only on average.
PIVOT (
    SELECT n, round(minimal_fraction, 5) AS "1/(n+1)",
           CASE WHEN method = 'ring' THEN 'ring_v' || points_per_bucket ELSE method END AS method,
           round(moved_fraction, 5) AS moved
    FROM h1
)
ON method IN ('jump', 'modulo', 'ring_v1', 'ring_v10', 'ring_v100', 'ring_v1000')
USING first(moved)
ORDER BY n;

.print
.print == H1. Moved over minimal: 1.0 is perfect. The bar shows log10 of the ratio for modulo
SELECT n,
       round(max(moved_over_minimal) FILTER (WHERE method = 'jump'), 3) AS jump,
       round(max(moved_over_minimal) FILTER (WHERE method = 'modulo'), 1) AS modulo,
       round(max(moved_over_minimal) FILTER (WHERE method = 'ring' AND points_per_bucket = 1), 3) AS ring_v1,
       round(max(moved_over_minimal) FILTER (WHERE method = 'ring' AND points_per_bucket = 1000), 3) AS ring_v1000,
       bar(log10(max(moved_over_minimal) FILTER (WHERE method = 'modulo')), 0, 3, 30) AS modulo_log10
FROM h1
GROUP BY n
ORDER BY n;

.print
.print == H1. Where the moved keys came from (misplaced = moved, but not onto the new bucket)
.print    Jump takes an even slice from every old bucket; a ring's new bucket takes from at most v donors.
.print    Donors are capped by the sample too: at n = 1000 only about 100 keys move.
SELECT method, points_per_bucket AS v,
       sum(misplaced) AS misplaced_total,
       max(donors) FILTER (WHERE n = 10) AS donors_n10,
       max(donors) FILTER (WHERE n = 100) AS donors_n100,
       max(donors) FILTER (WHERE n = 1000) AS donors_n1000,
       round(max(max_donor_share) FILTER (WHERE n = 100), 3) AS top_donor_share_n100,
       round(avg(abs(moved_over_minimal - 1)), 3) AS mean_abs_error_vs_minimal
FROM h1
GROUP BY method, points_per_bucket
ORDER BY method = 'ring', method, v;

.print
.print == H2. Balance over n buckets (1,000,000 keys): spread of keys per bucket
.print    stddev_ratio = measured stddev / binomial √(K·p·(1-p)); 1.0 is as even as perfect randomness.
.print    keyspace σ/μ is the ring's exact share of the circle, comparable with the paper's Figure 2.
SELECT n,
       CASE WHEN method = 'ring' THEN 'ring v=' || points_per_bucket ELSE method END AS method,
       round(min_over_mean, 3) AS min_over_mean,
       round(max_over_mean, 3) AS max_over_mean,
       round(stddev_ratio, 2) AS stddev_ratio,
       round(std_err, 4) AS "sampled σ/μ",
       round(keyspace_std_err, 4) AS "keyspace σ/μ",
       paper_std_err AS "paper σ/μ",
       state_bytes,
       bar(least(stddev_ratio, 40), 0, 40, 24) AS imbalance
FROM h2
ORDER BY n, method = 'ring', method, points_per_bucket;

.print
.print == H3. Loop iterations per lookup vs n: measured vs H_n (expected) vs the paper's bound ln(n)+1
.print    A ring lookup's binary search would take about log2(points) steps instead.
SELECT n,
       round(mean_iterations, 3) AS measured,
       round(expected_iterations, 3) AS "H_n",
       round(mean_over_expected, 4) AS ratio,
       round(paper_bound, 3) AS "ln(n)+1",
       round(log2_n, 1) AS log2_n,
       round(stddev_iterations, 2) AS stddev,
       max_iterations AS max,
       bar(mean_iterations, 0, (SELECT max(mean_iterations) FROM h3), 30) AS curve
FROM h3
ORDER BY n;

.print
.print == H3. Least-squares fit of mean iterations to a·ln(n) + b (n ≥ 2); theory: a = 1, b → γ ≈ 0.5772
SELECT model, round(slope_a, 4) AS a, round(intercept_b, 4) AS b, round(r_squared, 6) AS r_squared,
       theory_a, round(theory_b, 4) AS theory_b, points
FROM h3_fit;

.print
.print == Across runs: the headline number of each experiment, to spot regressions
SELECT run.run_id, run.git_commit,
       (SELECT round(max(abs(moved_over_minimal - 1)), 3) FROM runs_of('h1_movement') e
         WHERE e.run_id = run.run_id AND e.method = 'jump') AS h1_jump_worst_error,
       (SELECT sum(misplaced) FROM runs_of('h1_movement') e
         WHERE e.run_id = run.run_id AND e.method = 'jump') AS h1_jump_misplaced,
       (SELECT round(stddev_ratio, 3) FROM runs_of('h2_balance') e
         WHERE e.run_id = run.run_id AND e.method = 'jump' AND e.n = 1000) AS h2_jump_ratio_n1000,
       (SELECT round(keyspace_std_err, 4) FROM runs_of('h2_balance') e
         WHERE e.run_id = run.run_id AND e.method = 'ring' AND e.n = 1000 AND e.points_per_bucket = 1000) AS h2_ring_v1000_std_err,
       (SELECT round(mean_iterations, 3) FROM runs_of('h3_jumps') e
         WHERE e.run_id = run.run_id AND e.n = 1048576) AS h3_iterations_2pow20,
       (SELECT round(slope_a, 3) FROM runs_of('h3_fit') e WHERE e.run_id = run.run_id) AS h3_fit_a
FROM runs run
ORDER BY run.run_id;
