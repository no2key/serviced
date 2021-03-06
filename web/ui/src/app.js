/* jshint unused: false */

// Copyright 2014 The Serviced Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// set this guy here to true to get waaaay
// more log messages. exciting!
var DEBUG = false;

/*******************************************************************************
 * Main module & controllers
 ******************************************************************************/
var controlplane = angular.module('controlplane', [
    'ngRoute', 'ngCookies','ngDragDrop','pascalprecht.translate', 'ngSanitize',
    'angularMoment', 'zenNotify', 'serviceHealth', 'ui.datetimepicker',
    'modalService', 'angular-cache', 'ui.codemirror', 'serviceActions',
    'sticky', 'graphPanel', 'servicesFactory', 'healthIcon', 'publicEndpointLink',
    'authService', 'miscUtils', 'hostsFactory', 'poolsFactory', 'instancesFactory', 'baseFactory',
    'ngTable', 'jellyTable', 'ngLocationUpdate', 'CCUIState', 'servicedConfig', 'areUIReady', 'log',
    'LogSearch', 'hostIcon', 'appName'
]);

controlplane.
    config(['$routeProvider', function($routeProvider) {
        $routeProvider.
            when('/login', {
                templateUrl: '/static/partials/login.html',
                controller: "LoginController"}).
            when('/logs', {
                templateUrl: '/static/partials/logs.html',
                controller: "LogController"}).
            when('/services/:serviceId', {
                templateUrl: '/static/partials/view-subservices.html',
                controller: "ServiceDetailsController"}).
            when('/apps', {
                templateUrl: '/static/partials/view-apps.html',
                controller: "AppsController"}).
            when('/hosts', {
                templateUrl: '/static/partials/view-hosts.html',
                controller: "HostsController",
                controllerAs: "hostsVM"}).
            when('/hosts/:hostId', {
                templateUrl: '/static/partials/view-host-details.html',
                controller: "HostDetailsController",
                controllerAs: "hostDetailsVM"}).
            when('/pools', {
                templateUrl: '/static/partials/view-pools.html',
                controller: "PoolsController",
                controllerAs: "poolsVM"}).
            when('/pools/:poolID', {
                templateUrl: '/static/partials/view-pool-details.html',
                controller: "PoolDetailsController"}).
            when('/status', {
                templateUrl: '/static/partials/view-status.html',
                controller: "StatusController"}).
            when('/backuprestore', {
                templateUrl: '/static/partials/view-backuprestore.html',
                controller: "BackupRestoreController"}).
            when('/internalservices', {
                templateUrl: '/static/partials/view-internal-services.html',
                controller: "InternalServicesController",
                controllerAs: "internalServicesVM"}).
            when('/internalservices/:id', {
                templateUrl: '/static/partials/view-internal-service-details.html',
                controller: "InternalServiceDetailsController",
                controllerAs: "internalServiceDetailsVM"}).
            otherwise({redirectTo: '/apps'});
    }]).
    config(['$translateProvider', function($translateProvider) {
        $translateProvider.useStaticFilesLoader({
            prefix: '/static/i18n/',
            suffix: '.json'
       });
        $translateProvider.preferredLanguage('en_US');
        $translateProvider.fallbackLanguage('en_US');
        $translateProvider.useSanitizeValueStrategy('sanitizeParameters');
    }]).
    config(['CacheFactoryProvider', function(CacheFactoryProvider){
        angular.extend(CacheFactoryProvider.defaults, {
            // Items will not be deleted until they are requested
            // and have expired
            deleteOnExpire: 'passive',

            // This cache will clear itself every hour
            cacheFlushInterval: 3600000,

            // This cache will sync itself with localStorage
            storageMode: 'memory'
         });
    }]).
    /**
     * Default Get requests to no caching
     **/
    config(["$httpProvider", function($httpProvider){
        //initialize get if not there
        if (!$httpProvider.defaults.headers.get) {
            $httpProvider.defaults.headers.get = {};
        }
        $httpProvider.defaults.headers.get['Cache-Control'] = 'no-cache';
        $httpProvider.defaults.headers.get['Pragma'] = 'no-cache';
        $httpProvider.defaults.headers.get['If-Modified-Since'] = 'Mon, 26 Jul 1997 05:00:00 GMT';
    }]).
    filter('treeFilter', function() {
        /*
         * @param items The array from ng-repeat
         * @param field Field on items to check for validity
         * @param validData Object with allowed objects
         */
        return function(items, field, validData) {
            if (!validData) {
                return items;
            }
            return items.filter(function(elem) {
                return validData[elem[field]] !== null;
            });
        };
    }).
    filter('toGB', function(){
        return function(input, hide){
            return (input/(1024*1024*1024)).toFixed(2) + (hide ? "": " GB");
        };
    }).
    filter('toMB', function(){
        return function(input, hide){
            return (input/(1024*1024)).toFixed(2) + (hide ? "": " MB");
        };
    }).
    filter('cut', function(){
        return function (value, wordwise, max, tail) {
            if (!value){
                return '';
            }

            max = parseInt(max, 10);
            if (!max){
                return value;
            }
            if (value.length <= max){
                return value;
            }

            value = value.substr(0, max);
            if (wordwise) {
                var lastspace = value.lastIndexOf(' ');
                if (lastspace !== -1) {
                    value = value.substr(0, lastspace);
                }
            }

            return value + (tail || ' …');
        };
    }).
    filter('prettyDate', function(){
        return function(dateString){
            return moment(new Date(dateString)).format('MMM Do YYYY, hh:mm:ss');
        };
    }).
    // create a human readable "fromNow" string from
    // a date. eg: "a few seconds ago"
    filter('fromNow', function(){
        return function(date){
            return moment(date).fromNow();
        };
    })
    .run(["$rootScope", "$window", "$location", "areUIReady", "log", "CCUIState",
    function($rootScope, $window, $location, areUIReady, log, CCUIState){
        // scroll to top of page on navigation
        $rootScope.$on("$routeChangeSuccess", function (event, currentRoute, previousRoute) {
            $window.scrollTo(0, 0);
        });

        var queryParams = $location.search(),
            config = CCUIState.get();

        // option to disable animation for
        // acceptance tests
        if(queryParams["disable-animation"] === "true"){
            $("body").addClass("no-animation");
            config.disableAnimation = true;
        }

        // set log level
        if(queryParams["loglevel"]){
            log.setLogLevel(log.level[queryParams["loglevel"]]);
            log.info(`set log level to ${queryParams["loglevel"]}`);
        }

        var loaderEl = $(".loading_wrapper"),
            isCleared = false;

        $rootScope.$on("ready", function(){
            var clearLoader = function(){
                loaderEl.remove();
                $("body").removeClass("loading");
                isCleared = true;
            };

            setTimeout(function(){
                if(!isCleared){
                    if(config.disableAnimation){
                        clearLoader();
                    } else {
                        loaderEl.addClass("hide_it").one("transitionend", clearLoader);
                    }
                }
            }, 1000);

        });

        // tiny but visible loading indicator
        // if the UI is busy
        var uiLockEl = $("<div class='uilock icon-spin' style='display: none;'></div>");
        $("body").append(uiLockEl);
        areUIReady.onLock = function(){
            uiLockEl.show();
        };
        areUIReady.onUnlock = function(){
            uiLockEl.hide();
        };
    }]);
