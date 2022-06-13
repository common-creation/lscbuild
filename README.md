# lscbuild
Lightweight, Simple, Configuable builder

## Install

### from release

Download from https://github.com/common-creation/lscbuild/releases and unarchive tar.gz.  

### go install

Require: go 1.18+

```bash
go install github.com/common-creation/lscbuild/cmd/lscbuild@latest
```

## YAML structure

For more information, please see wiki  
https://github.com/common-creation/lscbuild/wiki/YAML-structure

```yaml
version: 1
jobs:
  build:
    steps:
      - cmd: rm -rf node_modules
        if:
          - directory:
              exists: ./node_modules
      - cmd: yarn install --frozen-lockfile
      - cmd: yarn build
```
