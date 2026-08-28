# Changelog

## Unreleased

- Initial prototype: reverse proxy implementing content negotiation
  (`Accept: text/markdown`) and bulk export as real, and identity
  verification / graduated rate limits with pillar 3 stubbed behind the
  `identity.Verifier` interface pending a real WebBotAuth-style registry.
