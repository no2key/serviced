(function () {
    'use strict';

    // share angular services outside of angular context
    let $notification, serviceHealth, $q, resourcesFactory, utils, Instance;

    controlplane.factory('Service', ServiceFactory);

    // DesiredState enum
    var START = 1,
        STOP = 0,
        RESTART = -1;

    // service types
    var ISVC = "isvc",           // internal service
        APP = "app",             // service with no parent
        META = "meta",           // service with children but no startup command
        DEPLOYING = "deploying"; // service whose parent is still being deployed

    function fetch(methodName, propertyName, force) {
        let deferred = $q.defer();

        if (this[propertyName] && !force) {
            deferred.resolve();
            return deferred.promise;
        }
        // NOTE: V2 API only
        // TODO: make sure methodName exists
        // TODO: error callback
        resourcesFactory.v2[methodName](this.id)
            .then(data => {
                // console.log(`fetched ${data.length} ${propertyName} from ${methodName} for id ${this.id}`);
                this[propertyName] = data;
                this.touch();
                deferred.resolve();
            },
            error => {
                deferred.reject(error);
                console.warn(error);
            });

        return deferred.promise;
    }

    function getDescendents(descendents, service) {
        if (service.subservices) {
            service.subservices.forEach(svc => getDescendents(descendents, svc));
        }
        descendents[service.id] = service;
        return descendents;
    }

    class Service {

        constructor(model) {
            this.subservices = [];
            this.instances = [];
            this.update(model);
        }

        update(model) {
            // basically new-up with exisiting children and instances
            this.name = model.Name;
            this.id = model.ID;
            this.desiredState = model.DesiredState;
            this.model = Object.freeze(model);
            this.evaluateServiceType();
            this.touch();   
        }

        evaluateServiceType() {
            // infer service type
            this.type = [];
            if (this.model.ID.indexOf("isvc-") !== -1) {
                this.type.push(ISVC);
            }

            if (!this.model.ParentServiceID) {
                this.type.push(APP);
            }

            if (this.subservices.length && !this.model.Startup) {
                this.type.push(META);
            }

            if (this.parent && this.parent.isDeploying()) {
                this.type.push(DEPLOYING);
            }
        }

        // fills out service object
        fetchAll(force) {
            this.fetchEndpoints(force);
            this.fetchAddresses(force);
            this.fetchConfigs(force);
            // forced fetch-children discards existing tree services
            this.fetchServiceChildren();
            this.fetchMonitoringProfile(force);
            this.fetchExportEndpoints(force);
        }

        fetchAddresses(force) {
            fetch.call(this, "getServiceIpAssignments", "addresses", force);
        }

        fetchConfigs(force) {
            fetch.call(this, "getServiceConfigs", "configs", force);
        }

        fetchEndpoints(force) {
            // populate publicEndpoints property
            fetch.call(this, "getServicePublicEndpoints", "publicEndpoints", force);
        }

        fetchExportEndpoints(force) {
            // fetch.call(this, "getServiceExportEndpoints", "exportedServiceEndpoints", force);
            let deferred = $q.defer();
            resourcesFactory.v2.getServiceExportEndpoints(this.id)
                .then(response => {
                    console.log(`fetched ${response.length} exportEndpoints for id ${this.id}`);
                    let tcpEndpoints = response.filter(function(ept) { return ept.Protocol === "tcp"; });
                    tcpEndpoints.forEach(ept => ept.Value = ept.ServiceName + " - " + ept.Application);
                    this.exportedServiceEndpoints = tcpEndpoints;
                    deferred.resolve();
                },
                error => {
                    console.warn(error);
                    deferred.reject();
                });

            return deferred.promise;
        }

        fetchMonitoringProfile(force) {
            fetch.call(this, "getServiceMonitoringProfile", "monitoringProfile", force);
        }

        fetchInstances() {
            let deferred = $q.defer();
            resourcesFactory.v2.getServiceInstances(this.id)
                .then(data => {
                    // console.log(`fetched ${data.length} instances for ${this.id}`);
                    this.instances = data.map(i => new Instance(i));
                    deferred.resolve();
                },
                error => {
                    console.warn(error);
                    deferred.reject();
                });

            return deferred.promise;
        }

        fetchServiceChildren(force) {
            let deferred = $q.defer();
            // fetch.call(this, "getServiceChildren", "subservices", force);
            if (this.subservices.length && !force) {
                deferred.resolve();
            }
            resourcesFactory.v2.getServiceChildren(this.id)
                .then(data => {
                    this.subservices = data.map(s => new Service(s));
                    this.touch();
                    deferred.resolve();
                },
                error => {
                    console.warn(error);
                    deferred.reject();
                });
            return deferred.promise;
        }

        // fast-moving state for this service and its instances
        // note: returns a promise that resolves with a single status object
        getStatus() {
            let deferred = $q.defer();

            resourcesFactory.v2.getServiceStatus(this.id)
                .then(results => {
                    if (results.length && !results[0].NotFound) {
                        deferred.resolve(results[0]);
                    } else {
                        deferred.reject(`Could not get service status for id ${this.id}`);
                    }
                }, error => {
                    deferred.reject(error);
                });
            return deferred.promise;
        }

        updateDescendentStatuses() {
            let deferred = $q.defer();

            let descendents = getDescendents({}, this);
            let ids = Object.keys(descendents);
            console.log(`Healthcheck: FETCH --------------- ${ids.length} services`);

            resourcesFactory.v2.getServiceStatuses(ids)
                .then(results => {
                    if (results.length) {
                        // iterate through results and call updateState(status) on each service
                        results.forEach(stat => {
                            let svc = descendents[stat.ServiceID];
                            svc.updateState(stat);
                            // console.log(`Healthcheck: ${descendents[stat.ServiceID].name}`);
                        });
                        deferred.resolve();
                    } else {
                        deferred.reject(`Healthcheck: ID not found ${this.id}`);
                    }
                }, error => {
                    deferred.reject(error);
                });
            return deferred.promise;
        }

        hasInstances() {
            return !!this.instances.length;
        }

        isIsvc() {
            return !!~this.type.indexOf(ISVC);
        }

        hasChildren() {
            return this.model.HasChildren;
        }

        resourcesGood() {
            for (var i = 0; i < this.instances.length; i++) {
                if (!this.instances[i].resourcesGood()) {
                    return false;
                }
            }
            return true;
        }

        // start, stop, or restart this service
        start(skipChildren) {
            var promise = resourcesFactory.startService(this.id, skipChildren),
                oldDesiredState = this.desiredState;

            this.desiredState = START;

            // if something breaks, return desired
            // state to its previous value
            return promise.error(() => {
                this.desiredState = oldDesiredState;
            });
        }

        stop(skipChildren) {
            var promise = resourcesFactory.stopService(this.id, skipChildren),
                oldDesiredState = this.desiredState;

            this.desiredState = STOP;

            // if something breaks, return desired
            // state to its previous value
            return promise.error(() => {
                this.desiredState = oldDesiredState;
            });
        }

        restart(skipChildren) {
            var promise = resourcesFactory.restartService(this.id, skipChildren),
                oldDesiredState = this.desiredState;

            this.desiredState = RESTART;

            // if something breaks, return desired
            // state to its previous value
            return promise.error(() => {
                this.desiredState = oldDesiredState;
            });
        }

        stopInstance(instance) {
            resourcesFactory.killRunning(instance.model.HostID, instance.id)
                .success(() => {
                    this.touch();
                })
                .error((data, status) => {
                    $notification.create("Stop Instance failed", data.Detail).error();
                });
        }


        // mark services updated to trigger render via $watch
        touch() {
            this.lastUpdate = new Date().getTime();
        }

        // kicks off request to update fast-moving instances and service state
        fetchAllStates() {
            return $q.all([this.fetchInstances(), this.getStatus()])
                .then(results => {
                    this.updateState(results[1]);
                }, error => {
                    console.log("Unable to update instance states");
                    // TODO: Error
                });
        }

        // updates service state and instances states        
        updateState(status) {

            // update service status
            this.desiredState = status.DesiredState;

            // update public endpoints
            if (this.publicEndpoints) {
                this.publicEndpoints.forEach(ept => {
                    if (ept.ServiceID === this.id) {
                        ept.desiredState = this.desiredState;
                    } else {
                        // console.log("Whose kid is this? Not mine!");
                    }
                });
            }
            let statusMap = {};
            status.Status.forEach(s => statusMap[s.InstanceID] = s);

            // update instance status
            this.instances.forEach(i => {
                let s = statusMap[i.model.InstanceID];
                // make sure status exists for instance
                if (!s) {
                    console.log(`Service instance ${i.model.ServiceName} has no instance`);
                    return;
                }
                i.updateState(s);
            });

            // TODO: pass myself into health status and get my health status back
            // update my health icon

            serviceHealth.update({ [this.id]: this });
            this.status = serviceHealth.get(this.id);
            // console.log(`Healthcheck: ${this.name} is ${this.status.status}`);
            this.touch();
        }

    }

    ServiceFactory.$inject = ['$notification', '$serviceHealth', '$q', 'resourcesFactory', 'miscUtils', 'Instance'];
    function ServiceFactory(_$notification, _serviceHealth, _$q, _resourcesFactory, _utils, _Instance) {

        // api access via angular context
        $notification = _$notification;
        serviceHealth = _serviceHealth;
        $q = _$q;
        resourcesFactory = _resourcesFactory;
        utils = _utils;
        Instance = _Instance;

        return Service;

    }
})();