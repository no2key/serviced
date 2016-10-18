/* HostsController
 * Displays details for a specific host
 */
(function(){
    "use strict";

    controlplane.controller("HostsController", ["$scope", "$routeParams", "$location",
        "$filter", "resourcesFactory", "authService", "$modalService",
        "$interval", "$timeout", "$translate", "$notification", "miscUtils", "hostsFactory",
        "poolsFactory", "servicesFactory", "areUIReady",
    function($scope, $routeParams, $location, $filter, resourcesFactory,
    authService, $modalService, $interval, $timeout, $translate, $notification,
    utils, hostsFactory, poolsFactory, servicesFactory, areUIReady){
        // Ensure logged in
        authService.checkLogin($scope);

        $scope.indent = utils.indentClass;

        $scope.resetNewHost = function(){
            $scope.newHost = {
                port: $translate.instant('placeholder_port')
            };
            if ($scope.pools && $scope.pools.length > 0){
                $scope.newHost.PoolID = $scope.pools[0].id;
            }
        };

        $scope.modalAddHost = function() {
            areUIReady.lock();
            $scope.resetNewHost();
            $modalService.create({
                templateUrl: "add-host.html",
                model: $scope,
                title: "add_host",
                actions: [
                    {
                        role: "cancel",
                    },{
                        role: "ok",
                        label: "btn_next",
                        icon: "glyphicon-chevron-right",
                        action: function(){
                            if(this.validate()){
                                // disable ok button, and store the re-enable function
                                var enableSubmit = this.disableSubmitButton();
                                if ($scope.newHost.RAMLimit === undefined || $scope.newHost.RAMLimit === '') {
                                    $scope.newHost.RAMLimit = "100%";
                                }

                                $scope.newHost.IPAddr = $scope.newHost.host + ':' + $scope.newHost.port;

                                resourcesFactory.addHost($scope.newHost)
                                    .success(function(data, status){
                                        $modalService.modals.displayHostKeys(data.PrivateKey, data.Registered, $scope.newHost.host);
                                        update();
                                    }.bind(this))
                                    .error(function(data, status){
                                        // TODO - form error highlighting
                                        this.createNotification("", data.Detail).error();
                                        // reenable button
                                        enableSubmit();
                                    }.bind(this));
                            }
                        }
                    }
                ],
                validate: function(){
                    var err = utils.validateHostName($scope.newHost.host, $translate) ||
                        utils.validatePortNumber($scope.newHost.port, $translate) ||
                        utils.validateRAMLimit($scope.newHost.RAMLimit);
                    if(err){
                        this.createNotification("Error", err).error();
                        return false;
                    }
                    return true;
                },
                onShow: () => {
                    areUIReady.unlock();
                }
            });
        };

        $scope.remove_host = function(hostId) {
            $modalService.create({
                template: $translate.instant("confirm_remove_host") + " <strong>"+ hostsFactory.get(hostId).name +"</strong>",
                model: $scope,
                title: "remove_host",
                actions: [
                    {
                        role: "cancel"
                    },{
                        role: "ok",
                        label: "remove_host",
                        classes: "btn-danger",
                        action: function(){

                            resourcesFactory.removeHost(hostId)
                                .success(function(data, status) {
                                    $notification.create("Removed host", hostId).success();
                                    // After removing, refresh our list
                                    update();
                                    this.close();
                                }.bind(this))
                                .error(function(data, status){
                                    $notification.create("Removing host failed", data.Detail).error();
                                    this.close();
                                }.bind(this));
                        }
                    }
                ]
            });
        };

        $scope.clickHost = function(hostId) {
            resourcesFactory.routeToHost(hostId);
        };

        $scope.clickPool = function(poolID) {
            resourcesFactory.routeToPool(poolID);
        };

        // TODO - move this to Host.js when v2 stuff drops
        // provide a way for hostIcon to get statuses
        $scope.getHostStatus = function(id){
            if($scope.hostStatuses){
                return $scope.hostStatuses[id];
            }
        };
        $scope.getHostStatusClass = function(host){
            let status = $scope.getHostStatus(host.id);

            // stuff hasnt loaded, so unknown
            if(!status){
                return "unknown";
            }

            let active = status.Active,
                authed = status.Authenticated;

            // connected and authenticated
            if(active && authed){
                return "passed";

            // connected but not yet authenticated
            } else if(active && !authed){
                // TODO - something more clearly related to auth
                return "unknown";

            // not connected
            } else {
                return "failed";
            }
        };


        function update(){
            hostsFactory.update()
                .then(() => {
                    $scope.hosts = hostsFactory.hostList;
                }, () => {
                    // wait a sec and try again
                    $timeout(update, 1000);
                });

            poolsFactory.update()
                .then(() => {
                    $scope.pools = poolsFactory.poolList;
                    $scope.resetNewHost();
                }, () => {
                    // wait a sec and try again
                    $timeout(update, 1000);
                });
        }

        function init(){
            $scope.name = "hosts";
            $scope.params = $routeParams;

            $scope.breadcrumbs = [
                { label: 'breadcrumb_hosts', itemClass: 'active' }
            ];

            $scope.hostsTable = {
                sorting: {
                    name: "asc"
                },
                watchExpression: function(){
                    return hostsFactory.lastUpdate;
                }
            };

            $scope.dropped = [];

            // update hosts
            update();

            // TODO - remove this and consolidate with v2
            // status polling
            $scope.hostStatusInterval = $interval(() => {
                resourcesFactory.getHostStatuses()
                    .then(data => {
                        let statuses = {};
                        data.forEach(s => statuses[s.HostID] = s);
                        $scope.hostStatuses = statuses;
                    }, err => {
                        console.log("err", err);
                    });
            }, 3000);

            servicesFactory.activate();
            hostsFactory.activate();
            poolsFactory.activate();
        }

        init();

        $scope.$on("$destroy", function(){
            hostsFactory.deactivate();
            servicesFactory.deactivate();
            poolsFactory.deactivate();
            $interval.cancel($scope.hostStatusInterval);
        });
    }]);
})();
