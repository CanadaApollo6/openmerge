name: CI
on:
  pull_request:
  push:
    branches: [main, 'openmerge/queue/**']
jobs:
  ci:
    runs-on: ubuntu-latest
    steps: [{run: 'true'}]
