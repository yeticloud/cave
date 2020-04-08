

angular.module('bunker', [])
    .controller('bunker', ['$scope', '$http', function ($scope, $http) {
        $scope.baseurl = "http://localhost:9000/api/v1/kv/"

        $scope.url = []
        $scope.keys = [
        ]
        $scope.activeItem = null

        $scope.getkeys = function (path) {
            if (path == '') {
                $scope.url = []
            } else {
                $scope.url = path.split("/")
            }
            $http({
                method: 'GET',
                url: $scope.baseurl + path
            }).then(
                function (res) {
                    console.log(res)
                    items = res.data
                    keys = []
                    for (k in res.data) {
                        if ($scope.isDir(res.data[k]) == "dir") {
                            keys.push({
                                "key": res.data[k],
                                "dir": true,
                            })
                        } else {
                            keys.push({
                                "key": res.data[k],
                                "dir": false,
                            })
                        }
                    }
                    $scope.keys = keys
                    return keys
                }, function (res) {
                    console.log(res)
                }
            )
        }

        $scope.clickKey = function (key) {
            uri = $scope.url.join("/") + key.key
            if (key.dir) {

                $scope.getkeys(uri)
                return
            } else {
                $http({
                    method: 'GET',
                    url: $scope.baseurl + uri,
                }).then(function (res) {
                    $scope.activeItem = JSON.stringify(res.data, undefined, 4)
                }, function (res) {
                    console.log(res)
                })
            }
        }

        $scope.isDir = function (item) {
            if (item.endsWith("/")) {
                return "dir"
            }
            return ""
        }

        $scope.stripSlash = function (item) {
            return item.replace("/", "")
        }

        $scope.sortKeys = function () {

        }
    }])