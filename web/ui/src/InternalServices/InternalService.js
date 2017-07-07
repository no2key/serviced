(function () {
    'use strict';

    let $serviceHealth, resourcesFactory, $q;

    controlplane.factory('InternalService', InternalServiceFactory);

    class InternalServiceInstance {

        constructor(data) {
            this.id = data.InstanceID;
            this.name = data.ServiceName;
            this.model = Object.freeze(data);

            this.touch();
        }

        touch() {
            this.lastUpdate = new Date().getTime();
        }

        update(data) {
            this.model = Object.freeze(data);
            // the instance model data comes in with health and
            // memory stats, so use that to do an initial instace
            // status update
            this.updateStatus({
                HealthStatus: data.HealthStatus,
                MemoryUsage: data.MemoryUsage
            });
        }

        updateStatus(status) {
            this.healthChecks = status.HealthStatus;
            this.touch();
        }
    }

    class InternalService {

        constructor(data){
            this.id = data.ID;
            this.name = data.Name;
            this.model = Object.freeze(data);
            this.instances = [];

            this.touch();
        }

        touch() {
            this.lastUpdate = new Date().getTime();
        }

        isIsvc() {
            return true;
        }

        fetchInstances() {
            //return resourcesFactory.v2.getInternalServiceInstances(this.id)
            //    .then(data => this.instances = data.map(i => new InternalServiceInstance(i)));
            let deferred = $q.defer();
            resourcesFactory.v2.getInternalServiceInstances(this.id)
                .then(results => {
                    results.forEach(data => {
                        // new-ing instances will cause UI bounce and force rebuilding
                        // of the popover. To minimize UI churn, update/merge status info
                        // into exisiting instance objects
                        let iid = data.InstanceID;
                        if (this.instances[iid]) {
                            this.instances[iid].update(data);
                        } else {
                            // add into the proper instance slot here
                            this.instances[iid] = new InternalServiceInstance(data);
                        }
                    });
                    // chop off any extraneous instances
                    this.instances.splice(results.length);
                    deferred.resolve();
                },
                error => {
                    console.warn(error);
                    deferred.reject();
                });

            return deferred.promise;
        }

        updateStatus(status) {
            this.desiredState = status.DesiredState;

            let statusMap = status.Status.reduce((map, s) => {
                map[s.InstanceID] = s;
                return map;
            }, {});

            this.instances.forEach(i => {
                let s = statusMap[i.id];
                if (s) {
                    i.updateStatus(s);
                } else {
                    console.log(`Could not find status for instance ${i.id}`);
                }
            });

            $serviceHealth.update({ [this.id]: this });
            this.status = $serviceHealth.get(this.id);
            this.touch();
        }
    }

    InternalServiceFactory.$inject = ['$serviceHealth', 'resourcesFactory', '$q'];
    function InternalServiceFactory(_$serviceHealth, _resourcesFactory, _$q) {

        $serviceHealth = _$serviceHealth;
        resourcesFactory = _resourcesFactory;
        $q = _$q;

        return InternalService;
    }

})();
