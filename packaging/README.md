# Packaging and app catalogs

Listings for the self-hosting app catalogs. Nothing here changes the server.

| Catalog | Files | Status |
|---|---|---|
| Unraid Community Applications | `ca_profile.xml` and `templates/gutter-server.xml` at the repo root | Ready to submit |
| CasaOS / ZimaOS app store | `packaging/casaos/GutterServer/` | Ready to submit |
| Homebrew | `packaging/homebrew/gutter-server.rb` | Ready for a tap |

`packaging/icon.png` (512 px) and `packaging/icon.svg` are the listing icon.

## Unraid Community Applications

CA reads the repo directly, which is why `ca_profile.xml` (required) and
`templates/` sit at the root next to `LICENSE` (an OSI licence is required).
The template uses `:latest`, so Unraid users get each tagged release.

To submit (after this is merged to `main`, because `TemplateURL` and `Icon`
point at `main`):

1. Make sure GitHub two-factor auth is on (CA policy for authors).
2. Go to https://ca.unraid.net/submit/new, enter this repo, fix anything the
   preview flags, run Validate and Scan, then submit for review.
3. Approved apps show up after the next catalog build.

No Unraid forum support thread is required; `Support` points at GitHub issues.

## CasaOS / ZimaOS app store

The official store is `IceWhaleTech/CasaOS-AppStore` (it also feeds ZimaOS).
To submit, fork it, copy `packaging/casaos/GutterServer/` to
`Apps/GutterServer/`, run `./scripts/build_dist.sh`, and open a PR against
`main` saying it is a new app and how it was tested.

- Submitted as IceWhaleTech/CasaOS-AppStore PR 1183. The image is pinned to
  `0.2.0`; bump `image`, `version` and `update_at` after each release, here
  and on the store.
- Category must be one of the store's fixed list; `Media` fits.
- Reviews are slow (months, by the store's own community pages). The BigBear
  store, preinstalled on CasaOS, is a faster second home and takes the same
  compose file in its own layout.

## Homebrew

homebrew-core needs 75 stars (225 for a self-submitted formula) and a repo
older than 30 days, so the formula goes in a personal tap first:

```sh
brew tap-new rthaines211/homebrew-tap      # creates the tap with bottle CI
cp packaging/homebrew/gutter-server.rb "$(brew --repo rthaines211/tap)/Formula/"
brew install --build-from-source rthaines211/tap/gutter-server
brew test gutter-server && brew audit --strict --new rthaines211/tap/gutter-server
```

The tap is live at `github.com/rthaines211/homebrew-tap`; after each release,
update the formula's `url` and `sha256` there and here. Users install with:

```sh
brew install rthaines211/tap/gutter-server
brew services start gutter-server
```

To bump to a new release, change `url` to the new tag and update `sha256`:

```sh
curl -sL https://github.com/rthaines211/gutter-server/archive/refs/tags/vX.Y.Z.tar.gz | shasum -a 256
```
