<a name="unreleased"></a>
## [Unreleased]


<a name="v3.1.0"></a>
## [v3.1.0] - 2019-09-09
### Chore
- update examples and changelog
- modify examples
- add go 1.11 to travis config

### Feat
- implement gtt orders api
- add models for gtt

### Fix
- travis config, update go versions
- change type of instrument token in orders response struct to uint32
- access token and api key getting appended multiple times in ticker
- ticker SetRootURL() method didn't set root URL

### Test
- fix test for Cancel Order

### Tests
- update test package, gtt tests

### Pull Requests
- Merge pull request [#11](https://github.com/zerodhatech/gokiteconnect/issues/11) from rhnvrm/master
- Merge pull request [#10](https://github.com/zerodhatech/gokiteconnect/issues/10) from rhnvrm/master


<a name="v3.0.0"></a>
## v3.0.0 - 2018-09-03
### Chore
- add travis ci config

### Connect_test
- Refactor setting up mock responders

### Feat
- convert package to go module
- fix tests and make struct members of instrument public

### Fix
- fix goversion in travis
- add custom go get command in travis
- travis config
- remove models.PlainResponse from user and portfolio calls

### Refactor
- calculate depthitem info
- minor cosmetic change

### Test
- added tests for errors

### Tests
- market

### Pull Requests
- Merge pull request [#7](https://github.com/zerodhatech/gokiteconnect/issues/7) from zerodhatech/gomod
- Merge pull request [#5](https://github.com/zerodhatech/gokiteconnect/issues/5) from rhnvrm/markettest
- Merge pull request [#4](https://github.com/zerodhatech/gokiteconnect/issues/4) from rhnvrm/errors_test
- Merge pull request [#3](https://github.com/zerodhatech/gokiteconnect/issues/3) from rhnvrm/master
- Merge pull request [#1](https://github.com/zerodhatech/gokiteconnect/issues/1) from mr-karan/tests
- Merge pull request [#2](https://github.com/zerodhatech/gokiteconnect/issues/2) from zerodhatech/travis


[Unreleased]: https://github.com/zerodhatech/gokiteconnect/compare/v3.1.0...HEAD
[v3.1.0]: https://github.com/zerodhatech/gokiteconnect/compare/v3.0.0...v3.1.0
