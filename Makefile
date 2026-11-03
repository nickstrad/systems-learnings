# Each top-level Go module provides check, e2e, and demo targets.
PROJECTS := $(patsubst %/go.mod,%,$(wildcard */go.mod))
TEST_TARGETS := $(addprefix test-,$(PROJECTS))
DEMO_TARGETS := $(addprefix demo-,$(PROJECTS))
# Projects with an experiments/ package. Each runs through
# shared/scripts/experiments.sh, which writes results/<run>/ in the project
# and prints the project's DuckDB queries (scripts/experiments.sql).
EXPERIMENT_PROJECTS := $(patsubst %/experiments/,%,$(wildcard */experiments/))
EXPERIMENT_TARGETS := $(addprefix experiments-,$(EXPERIMENT_PROJECTS))

.DEFAULT_GOAL := help
.PHONY: help test demo experiments $(TEST_TARGETS) $(DEMO_TARGETS) $(EXPERIMENT_TARGETS)

help:
	@printf '%s\n' \
		'make test             Run every project test suite (check + e2e)' \
		'make test-<project>   Run one project test suite' \
		'make demo             Run every project demo in sequence' \
		'make demo-<project>   Run one project demo' \
		'make experiments      Run every project'"'"'s experiments, then its DuckDB queries' \
		'make experiments-<project> [RUN=pattern]' \
		'                      Run one project'"'"'s experiments; RUN picks some, e.g. RUN=H1' \
		'Projects: $(PROJECTS)' \
		'With experiments: $(EXPERIMENT_PROJECTS)'

test: $(TEST_TARGETS)

$(TEST_TARGETS): test-%:
	$(MAKE) -C $* check e2e

# Run demos sequentially because projects may use the same local ports.
demo:
	@set -e; for project in $(PROJECTS); do $(MAKE) -C "$$project" demo; done

$(DEMO_TARGETS): demo-%:
	$(MAKE) -C $* demo

experiments: $(EXPERIMENT_TARGETS)

$(EXPERIMENT_TARGETS): experiments-%:
	./shared/scripts/experiments.sh $* "$(RUN)"
