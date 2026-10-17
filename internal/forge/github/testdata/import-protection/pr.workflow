on: [pull_request]
jobs:
  pr-only:
    runs-on: ubuntu-latest
    steps: [{run: 'true'}]
