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
| `form:<FieldName>` | first value of the given field from the request body (see below) |

## Entry options

| Option | Description |
| --- | --- |
| `trimSpace` | strip leading/trailing whitespace from the resolved value |
| `lowercase` | lowercase the resolved value |
| `default` | value to use if the source can not be resolved (or is empty). Without a default the whole descriptor is dropped and the request is not limited by it |

## Form values

`form:<FieldName>` reads fields from `application/x-www-form-urlencoded` and `multipart/form-data` request bodies
(file parts are ignored). Query string parameters are **not** considered. The body is buffered up to `maxBodyBytes`
(default 1MiB) and handed unchanged to the upstream service.

If the body uses another content type, is larger than `maxBodyBytes`, or the field is missing, the value is unresolved.
When limiting sensitive endpoints (e.g. login or password reset) set a `default` so that clients can not
bypass the limit by sending oversized or malformed bodies, and normalize the value the same way the backend does.
Keep a per-IP descriptor alongside it: the per-user limit stops hammering a single account, the per-IP limit stops
one client from cycling through many usernames. A request is limited if any descriptor is over its limit.

```yaml
descriptors:
# per account
- entries:
  - key: reset_user
    from: form:username
    trimSpace: true
    lowercase: true
    default: __unresolved__
# per client IP
- entries:
  - key: remote_address
    from: remote_address
```

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
      maxBodyBytes: 1048576
      descriptors:
      - entries:
        - key: namespace
          from: value:namespace=production
        - key: subject
          from: header:X-Subject
```
