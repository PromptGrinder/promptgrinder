# Release policy

PromptGrinder uses semantic versioning. Documented v1 commands and successful
task formats remain compatible through v1.x unless a security or data-loss
defect requires change. JSON output is public: fields may be added, but existing
meanings do not silently change. Persisted formats remain readable or fail with
precise migration instructions.

Releases are built only from a reviewed, clean commit named by an annotated
semantic-version tag. Automation creates a draft release; a maintainer reviews
qualification evidence before publication. A release contains native
`promptgrinder_<version>_darwin_arm64.tar.gz` and
`promptgrinder_<version>_darwin_amd64.tar.gz` archives, `checksums.txt`, the
license, README, and build/provenance metadata. Each archive contains a native
binary named `promptgrinder`.

The final gate records exact qualified macOS and application versions, commands
and results, checksums, clean-machine walkthroughs, limitations, upgrade and
rollback steps, and reviewed scanner findings. No unmeasured compatibility,
signing, notarization, or Homebrew claim may appear in release notes.

Patch releases fix compatible defects. Minor releases add compatible behavior.
Major releases may change compatibility contracts and require migration
documentation. Security advisories may use an embargoed private branch and
coordinated release.
