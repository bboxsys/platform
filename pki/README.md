Getting Started Tasks:
[x] Generate private key.
[x] Generate self-signed certificate for bootstrap. 
[ ] Generate root certificate. 
[ ] Generate intermediate certificate. 
[ ] Storage mechanism based on interface{}. Pluggable set of storage backends.
[ ] Take the root ca offline and securely store keys.

Differentiators:
- Modern REST API.
- Opinionated defaults and minimal number of knobs.
- Reporting - know the state of current PKI deployment.
- Automated issuing, distribution, and renewal of certificates without relying
  on third parties. This requires and agent.
- Make it simple for configuration managmenet tools to take on this capability (but
  no depend on them).
- Report on certificate usage, pulling from OS-native logging where feasible.
- Integration with OS-native certificate storage mechanisms.

Research:
- Key escrow capabilities.
