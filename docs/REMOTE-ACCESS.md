# Remote access

Never port-forward plain HTTP to the internet. Your password would cross the
network in the clear. Pick one of these.

## Option 1: Tailscale

Install [Tailscale](https://tailscale.com) on the server and on your iPad or
iPhone, sign in to the same tailnet, and add the server's tailnet address
(`http://<tailscale-name>:8081`) in Gutter. Nothing is exposed to the internet,
and the traffic is encrypted.

## Option 2: Reverse proxy with TLS

Put a proxy with a real certificate in front of Gutter and expose only that.
With [Caddy](https://caddyserver.com) (it gets the certificate for you):

```
comics.example.com {
    reverse_proxy localhost:8081
}
```

Point a DNS record for `comics.example.com` at your server, open ports 80 and
443, and add `https://comics.example.com` in Gutter. Keep `GUTTER_PASSWORD`
long and unique.
