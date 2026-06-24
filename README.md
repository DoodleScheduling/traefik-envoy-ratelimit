# Traefik envoy ratelimit Adapter

[![release](https://img.shields.io/github/release/DoodleScheduling/traefik-envoy-ratelimit/all.svg)](https://github.com/DoodleScheduling/traefik-envoy-ratelimit/releases)
[![report](https://goreportcard.com/badge/github.com/DoodleScheduling/traefik-envoy-ratelimit)](https://goreportcard.com/report/github.com/DoodleScheduling/traefik-envoy-ratelimit)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/DoodleScheduling/traefik-envoy-ratelimit/badge)](https://api.securityscorecards.dev/projects/github.com/DoodleScheduling/traefik-envoy-ratelimit)
[![Coverage Status](https://coveralls.io/repos/github/DoodleScheduling/traefik-envoy-ratelimit/badge.svg?branch=master)](https://coveralls.io/github/DoodleScheduling/traefik-envoy-ratelimit?branch=master)
[![license](https://img.shields.io/github/license/DoodleScheduling/traefik-envoy-ratelimit.svg)](https://github.com/DoodleScheduling/traefik-envoy-ratelimit/blob/master/LICENSE)


A traefik plugin which invokes [envoy/ratelimit](https://github.com/envoyproxy/ratelimit) for ratelimiting.
The vanilla ratelimit plugin from traefik is minimalistic and has its limits (and certainly use cases).
However if more control is needed for ratelimiting it quickly limits the possibilities.
This middleware plugin invokes envoys ratelimit service instead. For instance this makes it possible to have cluster wide ratelimit rules over
multiple routes managed by traefik. Similar what the distributed ratelimit from traefik enterprise does.

**Note**: This has nothing to do with envoy directly. The ratelimitservice is a separate service provided by envoyproxy. It has no dependency to envoy directly.

# Descriptor mapping
| Key | Description | 
| --- | --- |
| `remote_address` | client IP |
| `path` | request URL path |
| `method` | HTTP method  |
| `host` | request host |
| `header:<HeaderName>` | value of the given request header |
| `value:<literal>` | a fixed literal value (great for grouping routes) |

# Configuration

Example usage

```yaml
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: envoy-ratelimit
spec:
  plugin:
    envoyratelimit:
      serviceUrl: "http://ratelimit:8080/json"
      domain: mydomain
      failOnError: true
      descriptors:
      - entries:
        - key: namespace
          from: value:namespace=production
        - key: subject
          from: header:X-Subject
```
