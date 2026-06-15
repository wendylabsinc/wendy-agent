ANSIBLE_CONFIG ?= ansible.cfg
ANSIBLE_GALAXY ?= ansible-galaxy
ANSIBLE_INVENTORY ?= ansible-inventory
ANSIBLE_PLAYBOOK ?= ansible-playbook

INVENTORIES ?= inventories/wendy.yml inventories/kb.yml
LIMIT ?=
EXTRA_ARGS ?=
ASK_PASS ?= auto
ASK_BECOME_PASS ?= auto
ANSIBLE_SSH_ARGS ?= -o ControlMaster=no -o ControlPath=none -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10
SSH_PUBLIC_KEY_FILE ?= $(HOME)/.ssh/id_ed25519.pub
DEPLOY_SSH_KEY ?= auto

INVENTORY_ARGS := $(foreach inventory,$(INVENTORIES),-i $(inventory))
LIMIT_ARG := $(if $(LIMIT),--limit $(LIMIT),)
ASK_PASS_ARGS = $(shell scripts/ssh-password-fallback.py $(INVENTORY_ARGS) $(LIMIT_ARG) --mode $(ASK_PASS))
ASK_BECOME_PASS_ARGS = $(shell scripts/sudo-become-fallback.py $(INVENTORY_ARGS) $(LIMIT_ARG) --mode $(ASK_BECOME_PASS))

ifeq ($(DEPLOY_SSH_KEY),false)
WENDY_ANSIBLE_SSH_PUBLIC_KEY_FILE :=
else ifeq ($(DEPLOY_SSH_KEY),auto)
ifneq ($(wildcard $(SSH_PUBLIC_KEY_FILE)),)
WENDY_ANSIBLE_SSH_PUBLIC_KEY_FILE := $(SSH_PUBLIC_KEY_FILE)
endif
else
WENDY_ANSIBLE_SSH_PUBLIC_KEY_FILE := $(SSH_PUBLIC_KEY_FILE)
endif

export ANSIBLE_CONFIG
export ANSIBLE_SSH_ARGS
export WENDY_ANSIBLE_SSH_PUBLIC_KEY_FILE

.PHONY: help install lint info deploy converge \
	deploy-ssh-key deploy-packages deploy-container-runtime deploy-swift deploy-runner \
	deploy-desktop deploy-power

help:
	@echo "Wendy Ansible automation"
	@echo ""
	@echo "Setup:"
	@echo "  make install              Install required Ansible collections"
	@echo ""
	@echo "Committed inventories:"
	@echo "  make lint                 Validate inventory and playbook syntax"
	@echo "  make info                 Report CI state without changes"
	@echo "  make deploy               Deploy CI desired state"
	@echo "  make converge             Run deploy twice to verify idempotency"
	@echo "  make deploy-ssh-key       Authorize SSH_PUBLIC_KEY_FILE for login"
	@echo ""
	@echo "Focused deployment ($(INVENTORIES)):"
	@echo "  make deploy-packages      Deploy CI packages/tools"
	@echo "  make deploy-container-runtime Deploy CI Docker/containerd runtime"
	@echo "  make deploy-swift         Deploy CI Swift toolchains"
	@echo "  make deploy-runner        Deploy CI runner integration"
	@echo "  make deploy-desktop       Deploy CI desktop access settings"
	@echo "  make deploy-power         Deploy CI power policy"
	@echo ""
	@echo "Overrides:"
	@echo "  make deploy INVENTORIES=inventories/wendy.yml"
	@echo "  make deploy INVENTORIES=inventories/kb.yml"
	@echo "  make deploy INVENTORIES=\"inventories/wendy.yml inventories/kb.yml\""
	@echo "  make deploy LIMIT=kb-cli-ubuntu-24-01"
	@echo "  make deploy ASK_PASS=false"
	@echo "  make deploy ASK_BECOME_PASS=false"
	@echo "  make deploy DEPLOY_SSH_KEY=false"
	@echo "  make deploy-ssh-key SSH_PUBLIC_KEY_FILE=\$$HOME/.ssh/wendy-ci.pub"
	@echo "  make deploy EXTRA_ARGS=\"--extra-vars 'github_runner_token=...'\""

install:
	$(ANSIBLE_GALAXY) collection install -r requirements.yml

lint:
	$(ANSIBLE_INVENTORY) $(INVENTORY_ARGS) --list >/dev/null
	$(ANSIBLE_PLAYBOOK) $(INVENTORY_ARGS) playbooks/info.yml --syntax-check
	$(ANSIBLE_PLAYBOOK) $(INVENTORY_ARGS) playbooks/ci.yml --syntax-check

info:
	$(ANSIBLE_PLAYBOOK) $(INVENTORY_ARGS) playbooks/info.yml --tags info $(LIMIT_ARG) $(ASK_PASS_ARGS) $(EXTRA_ARGS)

deploy:
	$(ANSIBLE_PLAYBOOK) $(INVENTORY_ARGS) playbooks/ci.yml $(LIMIT_ARG) $(ASK_PASS_ARGS) $(ASK_BECOME_PASS_ARGS) $(EXTRA_ARGS)

converge:
	$(MAKE) deploy INVENTORIES="$(INVENTORIES)" LIMIT="$(LIMIT)" EXTRA_ARGS="$(EXTRA_ARGS)" DEPLOY_SSH_KEY="$(DEPLOY_SSH_KEY)" SSH_PUBLIC_KEY_FILE="$(SSH_PUBLIC_KEY_FILE)"
	$(MAKE) deploy INVENTORIES="$(INVENTORIES)" LIMIT="$(LIMIT)" EXTRA_ARGS="$(EXTRA_ARGS)" DEPLOY_SSH_KEY="$(DEPLOY_SSH_KEY)" SSH_PUBLIC_KEY_FILE="$(SSH_PUBLIC_KEY_FILE)"

deploy-ssh-key:
	@test -f "$(SSH_PUBLIC_KEY_FILE)" || { \
		echo "SSH public key not found: $(SSH_PUBLIC_KEY_FILE)"; \
		echo "Create one with: ssh-keygen -t ed25519 -f \"$${HOME}/.ssh/id_ed25519\""; \
		exit 1; \
	}
	WENDY_ANSIBLE_SSH_PUBLIC_KEY_FILE="$(SSH_PUBLIC_KEY_FILE)" \
		$(ANSIBLE_PLAYBOOK) $(INVENTORY_ARGS) playbooks/ci.yml --tags ssh $(LIMIT_ARG) $(ASK_PASS_ARGS) $(EXTRA_ARGS)

deploy-packages:
	$(ANSIBLE_PLAYBOOK) $(INVENTORY_ARGS) playbooks/ci.yml --tags packages $(LIMIT_ARG) $(ASK_PASS_ARGS) $(ASK_BECOME_PASS_ARGS) $(EXTRA_ARGS)

deploy-container-runtime:
	$(ANSIBLE_PLAYBOOK) $(INVENTORY_ARGS) playbooks/ci.yml --tags container_runtime $(LIMIT_ARG) $(ASK_PASS_ARGS) $(ASK_BECOME_PASS_ARGS) $(EXTRA_ARGS)

deploy-swift:
	$(ANSIBLE_PLAYBOOK) $(INVENTORY_ARGS) playbooks/ci.yml --tags swift $(LIMIT_ARG) $(ASK_PASS_ARGS) $(ASK_BECOME_PASS_ARGS) $(EXTRA_ARGS)

deploy-runner:
	$(ANSIBLE_PLAYBOOK) $(INVENTORY_ARGS) playbooks/ci.yml --tags runner $(LIMIT_ARG) $(ASK_PASS_ARGS) $(ASK_BECOME_PASS_ARGS) $(EXTRA_ARGS)

deploy-desktop:
	$(ANSIBLE_PLAYBOOK) $(INVENTORY_ARGS) playbooks/ci.yml --tags desktop_access $(LIMIT_ARG) $(ASK_PASS_ARGS) $(ASK_BECOME_PASS_ARGS) $(EXTRA_ARGS)

deploy-power:
	$(ANSIBLE_PLAYBOOK) $(INVENTORY_ARGS) playbooks/ci.yml --tags power_policy $(LIMIT_ARG) $(ASK_PASS_ARGS) $(ASK_BECOME_PASS_ARGS) $(EXTRA_ARGS)
