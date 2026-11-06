# Contributing to dzstudio-collab-server

Issues and pull requests are welcome. Questions are also welcome in
[#dzstudio on Discord](https://discord.gg/fxyev4w9yZ).

## Pull requests

- Keep a pull request to one change and say in its description what it changes for someone running the server or
  writing a client.
- The core follows the protocol ([PROTOCOL.md](PROTOCOL.md)). A change that touches protocol behaviour names the
  section it implements; if the protocol is unclear there, say how you read it (see [QUESTIONS.md](QUESTIONS.md)).
- The core knows no operator. Anything specific to accounts, rights or storage belongs in an adapter behind one of the
  ports in `core/ports.go`.
- Run `make test` (go vet, unit tests with `-race`, then the conformance suite against a fresh binary) and say that it
  passed.

## Sign-off (DCO)

Contributions are accepted under the [Developer Certificate of Origin 1.1](https://developercertificate.org/): by
signing off a commit you confirm that you wrote it or otherwise have the right to submit it under the project's
license. Sign off every commit:

```bash
git commit -s -m "Answer client_outdated on session/join"
```

which adds a line `Signed-off-by: Your Name <you@example.org>` to the message. A check on every pull request looks for
it.

## License

By contributing you agree that your contribution is licensed under the
[GNU Affero General Public License v3.0](LICENSE), like the rest of the repository.
