ANSIBLE_CONFIG ?= ansible.cfg
ANSIBLE_GALAXY ?= ansible-galaxy
ANSIBLE_INVENTORY ?= ansible-inventory
ANSIBLE_PLAYBOOK ?= ansible-playbook

CI_INVENTORY ?= inventories/ci.yml
LIMIT ?=
EXTRA_ARGS ?=

LIMIT_ARG := $(if $(LIMIT),--limit $(LIMIT),)

export ANSIBLE_CONFIG

.PHONY: help install lint info deploy converge \
	ci-lint ci-info ci-deploy ci-converge \
	ci-deploy-packages ci-deploy-swift ci-deploy-runner \
	ci-deploy-desktop ci-deploy-power

help:
	@echo "Wendy Ansible automation"
	@echo ""
	@echo "Setup:"
	@echo "  make install              Install required Ansible collections"
	@echo ""
	@echo "Committed CI inventory:"
	@echo "  make lint                 Validate inventory and playbook syntax"
	@echo "  make info                 Report CI state without changes"
	@echo "  make deploy               Deploy CI desired state"
	@echo "  make converge             Run deploy twice to verify idempotency"
	@echo ""
	@echo "CI inventory ($(CI_INVENTORY)):"
	@echo "  make ci-lint              Validate CI inventory and playbook syntax"
	@echo "  make ci-info              Report CI state without changes"
	@echo "  make ci-deploy            Deploy CI desired state"
	@echo "  make ci-converge          Run CI deploy twice"
	@echo "  make ci-deploy-packages   Deploy CI packages/tools"
	@echo "  make ci-deploy-swift      Deploy CI Swift toolchains"
	@echo "  make ci-deploy-runner     Deploy CI runner integration"
	@echo "  make ci-deploy-desktop    Deploy CI desktop access settings"
	@echo "  make ci-deploy-power      Deploy CI power policy"
	@echo ""
	@echo "Overrides:"
	@echo "  make ci-deploy CI_INVENTORY=inventories/local.yml"
	@echo "  make ci-deploy LIMIT=kb-developer-ubuntu-24-01"
	@echo "  make ci-deploy EXTRA_ARGS=\"--extra-vars 'github_runner_token=...'\""

install:
	$(ANSIBLE_GALAXY) collection install -r requirements.yml

lint: ci-lint

info: ci-info

deploy: ci-deploy

converge: ci-converge

ci-lint:
	$(ANSIBLE_INVENTORY) -i $(CI_INVENTORY) --list >/dev/null
	$(ANSIBLE_PLAYBOOK) -i $(CI_INVENTORY) playbooks/info.yml --syntax-check
	$(ANSIBLE_PLAYBOOK) -i $(CI_INVENTORY) playbooks/ci.yml --syntax-check

ci-info:
	$(ANSIBLE_PLAYBOOK) -i $(CI_INVENTORY) playbooks/info.yml --tags info $(LIMIT_ARG) $(EXTRA_ARGS)

ci-deploy:
	$(ANSIBLE_PLAYBOOK) -i $(CI_INVENTORY) playbooks/ci.yml $(LIMIT_ARG) $(EXTRA_ARGS)

ci-converge:
	$(MAKE) ci-deploy CI_INVENTORY=$(CI_INVENTORY) LIMIT="$(LIMIT)" EXTRA_ARGS="$(EXTRA_ARGS)"
	$(MAKE) ci-deploy CI_INVENTORY=$(CI_INVENTORY) LIMIT="$(LIMIT)" EXTRA_ARGS="$(EXTRA_ARGS)"

ci-deploy-packages:
	$(ANSIBLE_PLAYBOOK) -i $(CI_INVENTORY) playbooks/ci.yml --tags packages $(LIMIT_ARG) $(EXTRA_ARGS)

ci-deploy-swift:
	$(ANSIBLE_PLAYBOOK) -i $(CI_INVENTORY) playbooks/ci.yml --tags swift $(LIMIT_ARG) $(EXTRA_ARGS)

ci-deploy-runner:
	$(ANSIBLE_PLAYBOOK) -i $(CI_INVENTORY) playbooks/ci.yml --tags runner $(LIMIT_ARG) $(EXTRA_ARGS)

ci-deploy-desktop:
	$(ANSIBLE_PLAYBOOK) -i $(CI_INVENTORY) playbooks/ci.yml --tags desktop_access $(LIMIT_ARG) $(EXTRA_ARGS)

ci-deploy-power:
	$(ANSIBLE_PLAYBOOK) -i $(CI_INVENTORY) playbooks/ci.yml --tags power_policy $(LIMIT_ARG) $(EXTRA_ARGS)
