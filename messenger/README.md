## Messenger 

Package for handling messaging between various endpoints.  

Each service should be accessible via Websocket for real-time use cases, or view REST API.

### websocket

Server package for websocket messaging with frontend.


### client

Client Package for connecting to messaging server. 

AuthN/AuthZ
* Organization: ACME
* Account: FINANCE
* User: WORKER

### server

Server package for messaging - based on NATS.io. For now, we are running this standalone vs. embedded
so we can focus.  Maybe come back to embedded version in Future. 
