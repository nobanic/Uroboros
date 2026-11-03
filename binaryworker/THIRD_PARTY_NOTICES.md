# Third-party notices

The Uroboros worker itself is distributed under AGPL-3.0-only; the image
includes that project license at `/usr/share/licenses/uroboros/LICENSE`.

The worker installs **LIEF 1.0.0** from the exact CPython 3.12 manylinux x86-64
wheel whose SHA-256 is recorded in `requirements.lock`. LIEF is distributed
under the Apache License 2.0. Upstream source and license:

- https://github.com/lief-project/LIEF/tree/1.0.0
- https://github.com/lief-project/LIEF/blob/1.0.0/LICENSE

The worker installs **capa 9.4.0** from the exact wheel whose SHA-256 is
recorded in `capa-requirements.lock`. The implementation is tied to source
commit `7a79f799a70f052e8382cdbeaec8115daba14a5b`. capa is distributed under the
Apache License 2.0. Upstream source and license:

- https://github.com/mandiant/capa/tree/v9.4.0
- https://github.com/mandiant/capa/blob/v9.4.0/LICENSE.txt

The embedded capa rules are tied to the 9.4.0 release commit
`2af9fbfc1c9b4634dbeb76b5d34fca9389fa7f80`. The three embedded FLIRT
signature files are from capa 9.4.0. Both corpora are Apache-2.0; their exact
content manifests are recorded in the worker protocol and verified during the
offline image build.

The remaining Python distributions form the exact, hash-locked static-Vivisect
closure in `capa-requirements.lock`. Their wheel metadata and bundled license
files remain in the final image for SBOM and license attribution. IDA-only
packages and the associated HTTP/update dependency chain are deliberately
excluded.

The worker also installs **FLOSS 3.2.0** from its exact PyPI wheel, tied to
upstream tag `v3.2.0` and commit
`69e8ea21ca78deb2418abb68f86c838d1d2672c7`. FLOSS is Apache-2.0. Its decoded
string path uses the same three reviewed FLIRT signatures as capa; their
shared content manifest is
`1b28cbecf167dbf458268ba0c282202d1347f663bba3a6089e0846391f3e083e`.

FLOSS and its separate dependency closure are recorded in
`floss-requirements.lock`. `binary2strings 0.1.13` and `halo 0.0.31` do not
publish suitable Linux CPython 3.12 wheels, so the two small reviewed wheels
in `floss-local-wheels` were built from PyPI sdists with SHA-256
`c6395fc97c4d908b36e08f5a558a79d371a843a8b308e21a0e2b489591877620`
and `7b67a3521ee91d53b7152d4ee3452811e1d2a6321975137762eb3d70063cc9d6`.
The build used the pinned Python base image, GCC/G++ 14.2.0, setuptools
83.0.0, wheel 0.46.3, pybind11 3.0.1, `SOURCE_DATE_EPOCH=1720396800`, and
stripped debug flags. Both are MIT licensed; exact notices are included.

The Python builder image is pinned by OCI digest, but the final image is a
minimal scratch filesystem. Copied operating-system library identities and
copyright files are retained. All Python and operating-system packages must be
captured by the image SBOM and license scan before activation; this source
notice is not a substitute for that runtime evidence.
