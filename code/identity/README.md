# Agent identity

Every action an agent takes involves three identities: the user it acts for, the agent itself, and the run (this session or task).
This example carries all three in a short-lived, scoped credential and follows it through one support run.

| File | What it holds |
| --- | --- |
| `token.go` | `Claims`, and `Issuer` and `Verifier`: a credential signed with `crypto/ed25519`, checked for signature and expiry |
| `narrow.go` | `Narrow`: a sub-agent's credential, with only scopes the parent holds and no later expiry |
| `transport.go` | `attach`: an `http.RoundTripper` that adds the credential to a tool's requests, so the model never sees it |
| `service.go` | A refund service that verifies the credential and checks its scope before acting, and the refund tool |
| `audit.go` | `Record`: for whom, which agent, which run, which credential, what was tried, and whether it was allowed |
| `main.go` | The demo |

## Run it

From the `code` directory, with no API key and no network beyond the local machine:

```console
$ go run ./identity
run run-7: agent support acting for u-42, scopes refund:42 read:orders
  refund 42: refunded customer 42
  refund 43: refund: refused: identity: scope not held: refund:43
narrow to refund:43: identity: scope not held: parent lacks refund:43
sub-agent reader, scopes read:orders, expires with its parent: true
  refund 42: refund: refused: identity: scope not held: refund:42
audit log:
  u-42    support run-7  cred-1 refund:42 allowed=true
  u-42    support run-7  cred-1 refund:43 allowed=false
  u-42    reader  run-7  cred-2 refund:42 allowed=false
```

The token format is a sketch: a real system uses a standard format, such as a JWT, issued by an authorization server, with an audience claim.
The guide is [Agent identity](https://golang-agents.com/production/agent-identity/).
