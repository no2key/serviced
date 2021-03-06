# Copyright 2015 The Serviced Authors.
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

UID = $(shell id -u)
GID = $(shell id -g)
PWD = $(shell pwd)

serviced_build_DOCKERDIR = ../../build
javascript_BLDDIR   = $(PWD)/build/js
controlplane_JS     = $(javascript_BLDDIR)/controlplane.js

docker_working_DIR = /mnt

GULP ?= /usr/bin/gulp

.PHONY: default
default: build

.PHONY: build
build: $(controlplane_JS)

#
# If you have NPM installed locally, you can run this step manually to
# refresh the NPM dependencies. If you have new dependencies to add to the
# regular build, check the README for details on how to refresh those
# dependencies in the build image.
#
# If you are using the serviced-build image to build the UI AND you need
# to refresh the NPM dependencies, then run the following commands:
# 'make buildServicedBuildImage;make pushServicedBuildImage' to
# rebuild and publish zenoss/serviced-build with the NPM dependencies.
#
npmInstall:
	if [ "$(NODEJS)" ]; then \
		./npmLink.sh; \
		npm install; \
	fi

$(controlplane_JS): src/*.js src/**/*.js
	@if [ "$(NODEJS)" ]; then \
		echo "Using local nodejs to run 'gulp release'"; \
		if [ -n "$(IN_DOCKER)" ]; then ./npmLink.sh; fi; \
		$(GULP) release; \
	else \
		echo "Using local zenoss/serviced-build:$(BUILD_VERSION) to run 'gulp release'"; \
		docker run --rm \
		-v $(PWD):$(docker_working_DIR) \
		-e UID_X=$(UID) \
		-e GID_X=$(GID) \
		$(USER_ARGS) \
		zenoss/serviced-build:$(BUILD_VERSION) \
		/bin/bash -c "source /root/userdo.sh \"cd $(docker_working_DIR) && ./npmLink.sh && $(GULP) release\""; \
	fi

.PHONY: test
test: build translation_test
	@if [ "$(NODEJS)" ]; then \
		echo "Using local nodejs to run 'gulp test'"; \
		if [ -n "$(IN_DOCKER)" ]; then ./npmLink.sh; fi; \
		$(GULP) test; \
	else \
		echo "Using local zenoss/serviced-build:$(BUILD_VERSION) to run 'gulp test'"; \
		docker run --rm \
		-v $(PWD):$(docker_working_DIR) \
		-e UID_X=$(UID) \
		-e GID_X=$(GID) \
		zenoss/serviced-build:$(BUILD_VERSION) \
		/bin/bash -c "source /root/userdo.sh \"cd $(docker_working_DIR) && ./npmLink.sh && $(GULP) test\""; \
	fi

.PHONY: translation_test
translation_test:
	echo "Checking translation files.."
	/bin/bash ./testTranslation.sh

.PHONY: clean
clean:
	@if [ "$(NODEJS)" ]; then \
		echo "Using local nodejs to run 'gulp clean'"; \
		if [ -n "$(IN_DOCKER)" ]; then ./npmLink.sh; fi; \
		$(GULP) clean; \
	else \
		echo "Using local zenoss/serviced-build:$(BUILD_VERSION) to run 'gulp clean'"; \
		docker run --rm \
		-v $(PWD):$(docker_working_DIR) \
		-e UID_X=$(UID) \
		-e GID_X=$(GID) \
		zenoss/serviced-build:$(BUILD_VERSION) \
		/bin/bash -c "source /root/userdo.sh \"cd $(docker_working_DIR) && ./npmLink.sh && $(GULP) clean\""; \
	fi
