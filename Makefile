# Each top-level Go module provides check, e2e, and demo targets.
PROJECTS := $(patsubst %/go.mod,%,$(wildcard */go.mod))
TEST_TARGETS := $(addprefix test-,$(PROJECTS))
DEMO_TARGETS := $(addprefix demo-,$(PROJECTS))

.DEFAULT_GOAL := help
.PHONY: help test demo $(TEST_TARGETS) $(DEMO_TARGETS)

help:
	@printf '%s\n' \
		'make test             Run every project test suite (check + e2e)' \
		'make test-<project>   Run one project test suite' \
		'make demo             Run every project demo in sequence' \
		'make demo-<project>   Run one project demo' \
		'Projects: $(PROJECTS)'

test: $(TEST_TARGETS)

$(TEST_TARGETS): test-%:
	$(MAKE) -C $* check e2e

# Run demos sequentially because projects may use the same local ports.
demo:
	@set -e; for project in $(PROJECTS); do $(MAKE) -C "$$project" demo; done

$(DEMO_TARGETS): demo-%:
	$(MAKE) -C $* demo
