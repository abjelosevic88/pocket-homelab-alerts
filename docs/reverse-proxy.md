# Reverse proxy

Set `BASE_URL` to the public address and `SECURE_COOKIES=true` behind HTTPS. See [deploy/reverse-proxy/Caddyfile](../deploy/reverse-proxy/Caddyfile). `/api/*` is reachable for Homepage widgets, shortcuts and linked apps; they send their own bearer token.
