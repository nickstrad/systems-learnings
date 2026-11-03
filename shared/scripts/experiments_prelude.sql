-- The setup every project's scripts/experiments.sql shares. A project
-- names the tables its experiments write, then loads this first:
--
--   SET VARIABLE tables = ['h1_movement', 'h2_balance', 'h3_jumps', 'h3_fit'];
--   .read ../shared/scripts/experiments_prelude.sql
--
-- It runs from the project directory and reads results/<run_id>/*.csv
-- (every CSV has run_id first; runs.csv describes the run). The run IDs
-- start with a UTC timestamp, so the largest is the newest. It defines:
--
--   runs_of('h1_movement')  every run's rows of results/*/h1_movement.csv
--   latest('h1_movement')   the rows of the latest run that wrote that table
--   runs                    every run, from results/*/runs.csv
--   latest_runs             each table's latest run, with its experiment
--                           (the table name up to the first '_': h3_fit is h3)
--
-- and prints the runs and the run each experiment's tables come from. A
-- run of some experiments only (RUN=H1) writes only their tables, so each
-- table shows its own latest run; tables one test writes together (h3 and
-- h3_fit) come from the same run.

.mode duckbox
.bail on

-- Every table needs at least one run in results/ before it can be read:
-- stop with a clear message rather than a missing-file error. (Its empty
-- result table is hidden; the error still prints.)
.output /dev/null
SELECT error('no results yet for ' || string_agg(name, ', ' ORDER BY name)
             || '; run every experiment once: make experiments (without RUN=)')
FROM (SELECT unnest(getvariable('tables')) || '.csv' AS name)
WHERE name NOT IN (SELECT parse_filename(file) FROM glob('results/*/*.csv'))
HAVING count(*) > 0;
.output

CREATE MACRO runs_of(name) AS TABLE
    FROM read_csv('results/*/' || name || '.csv');
CREATE MACRO latest(name) AS TABLE
    FROM runs_of(name) QUALIFY run_id = max(run_id) OVER ();

CREATE TEMP VIEW runs AS
    FROM read_csv('results/*/runs.csv', types = {'started_utc': 'VARCHAR'});
CREATE TEMP TABLE latest_runs AS
    SELECT split_part(name, '_', 1) AS experiment, name AS "table", max(run_id) AS run_id
    FROM (SELECT parse_filename(file, true) AS name, parse_filename(parse_dirpath(file)) AS run_id
          FROM glob('results/*/*.csv'))
    WHERE list_contains(getvariable('tables'), name)
    GROUP BY name;

.print
.print == Runs in results/ (newest last)
SELECT run_id, started_utc, git_commit, go_version
FROM runs
ORDER BY run_id;

.print
.print == The run each experiment's tables below come from (its latest)
SELECT DISTINCT experiment, run_id FROM latest_runs ORDER BY experiment, run_id;
