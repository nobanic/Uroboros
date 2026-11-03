# Accepted B0.5 binary runtime archives

These Docker archives are tracked as regular Git files and included in a
repository clone or GitHub-generated source ZIP. `clean/start.ps1` verifies
the archive checksum before loading it and then verifies the expected image digest.

| Archive | Bytes | SHA-256 | Loaded image |
| --- | ---: | --- | --- |
| `binary-broker-b05.tar` | 2,916,352 | `f12520c790e04e3cddb0766ab8cc93213563c78c61a28f5874b61eeb6513ef6f` | `uroboros-b0/binary-broker@sha256:0fc4e1bfe1c398f2371aa7b0f18c8b6a2fdbf3e3544b6ea3c3576fc734f58173` |
| `binary-worker-b05.tar` | 83,383,296 | `5cc2a489c15b2a85f8a65acaede4fc087040539840f330aa6617191dbb060123` | `uroboros-b0/binary-worker@sha256:142bdcf03aa22e6a27fead6dedf89391155a46210375f007871bd09726315514` |
