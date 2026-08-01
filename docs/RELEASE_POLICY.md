# Release policy

PromptGrinder uses semantic versioning. Documented v1 commands and successful
task formats remain compatible through v1.x unless a security or data-loss
defect requires change. JSON output is public: fields may be added, but existing
meanings do not silently change. Persisted formats remain readable or fail with
precise migration instructions.

Releases originate only from a reviewed, clean commit named by an annotated
semantic-version tag. Automation qualifies the tag and creates a draft GitHub
release with generated notes; a maintainer reviews the evidence before
publication. Beginning with `v1.0.0-rc.2.2`, PromptGrinder attaches no compiled
binary archives, binary-only checksums, or build metadata. GitHub's automatic
**Source code (zip)** and **Source code (tar.gz)** links remain available for
the tag and are intentionally used by source-based distribution such as
Homebrew.

The final gate records exact qualified macOS and application versions, commands
and results, source revision, clean-machine walkthroughs, limitations, upgrade
and rollback steps, and reviewed scanner findings. macOS installation will be
supported through a reviewed Homebrew formula that builds from the tagged
source; release notes must not claim Homebrew availability before that formula
is qualified.

Patch releases fix compatible defects. Minor releases add compatible behavior.
Major releases may change compatibility contracts and require migration
documentation. Security advisories may use an embargoed private branch and
coordinated release.
