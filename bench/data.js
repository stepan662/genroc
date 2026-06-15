window.BENCHMARK_DATA = {
  "lastUpdate": 1781559092016,
  "repoUrl": "https://github.com/stepan662/gent-go",
  "entries": {
    "gent throughput": [
      {
        "commit": {
          "author": {
            "email": "granat.stepan@gmail.com",
            "name": "Štěpán Granát",
            "username": "stepan662"
          },
          "committer": {
            "email": "granat.stepan@gmail.com",
            "name": "Štěpán Granát",
            "username": "stepan662"
          },
          "distinct": true,
          "id": "e996458917d3bfeceeadf6ddad0c0257843c0e7d",
          "message": "chore: better bench",
          "timestamp": "2026-06-15T23:28:32+02:00",
          "tree_id": "92b4566e1b3a36a0247ce0e257f8e52127c73aa4",
          "url": "https://github.com/stepan662/gent-go/commit/e996458917d3bfeceeadf6ddad0c0257843c0e7d"
        },
        "date": 1781559091025,
        "tool": "customBiggerIsBetter",
        "benches": [
          {
            "name": "spawn deep sqlite",
            "value": 92,
            "unit": "inst/s",
            "extra": "AMD EPYC 9V74 80-Core Processor · 4 cores · 16GB · linux x64 6.17.0-1018-azure"
          },
          {
            "name": "spawn deep postgres",
            "value": 311,
            "unit": "inst/s",
            "extra": "AMD EPYC 9V74 80-Core Processor · 4 cores · 16GB · linux x64 6.17.0-1018-azure"
          },
          {
            "name": "spawn recursive sqlite",
            "value": 94,
            "unit": "inst/s",
            "extra": "AMD EPYC 9V74 80-Core Processor · 4 cores · 16GB · linux x64 6.17.0-1018-azure"
          },
          {
            "name": "spawn recursive postgres",
            "value": 478,
            "unit": "inst/s",
            "extra": "AMD EPYC 9V74 80-Core Processor · 4 cores · 16GB · linux x64 6.17.0-1018-azure"
          }
        ]
      }
    ]
  }
}