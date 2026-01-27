# Flomation API Package Build System

This repository uses a template-based build system that generates both RPM and DEB packages from a single metadata configuration file.

## Metadata Configuration

All package metadata is defined in `project-metadata.json` at the root of the repository.

### Metadata Structure

```json
{
  "package": {
    "component_name": "api",
    "name": "flomation-api",
    "summary": "Flomation Automate API Server",
    "description": "Flomation Automate API Server - Backend API service for automation workflows",
    "license": "MIT",
    "url": "https://flomation.co"
  },
  "maintainer": {
    "name": "Build System",
    "email": "build@flomation.co"
  },
  "architecture": {
    "rpm": "x86_64",
    "deb": "amd64"
  }
}
```

## Metadata Field Usage

### Package Fields

| Field | Used In | Purpose |
|-------|---------|---------|
| `package.component_name` | RPM Spec, DEB Scripts, Systemd Service | Short component identifier (e.g., "api") used in paths like `/opt/flomation/api` |
| `package.name` | RPM Spec, DEB Control, Changelog | Full package name (e.g., "flomation-api") |
| `package.summary` | RPM Spec, DEB Control, Systemd Service | One-line package description |
| `package.description` | RPM Spec, DEB Control | Detailed package description |
| `package.license` | RPM Spec | Software license (e.g., "MIT") |
| `package.url` | RPM Spec, DEB Control | Project homepage URL |

### Maintainer Fields

| Field | Used In | Purpose |
|-------|---------|---------|
| `maintainer.name` | RPM Spec, DEB Control, Changelog | Package maintainer name |
| `maintainer.email` | RPM Spec, DEB Control, Changelog | Package maintainer email |

### Architecture Fields

| Field | Used In | Purpose |
|-------|---------|---------|
| `architecture.rpm` | RPM Spec | Target architecture for RPM packages (e.g., "x86_64") |
| `architecture.deb` | DEB Control | Target architecture for DEB packages (e.g., "amd64") |

## Generated Files

The `scripts/inject-metadata.sh` script reads `project-metadata.json` and generates:

### 1. RPM Spec File (`${PACKAGE_NAME}.spec`)

Generated from: `templates/yum/template.spec`

**Metadata Usage:**
- `Name:` → `package.name`
- `Summary:` → `package.summary`
- `%description` → `package.description`
- `License:` → `package.license`
- `URL:` → `package.url`
- `%define component_name` → `package.component_name`
- `BuildArch:` → `architecture.rpm`
- `%changelog` maintainer → `maintainer.name` and `maintainer.email`

### 2. Systemd Service File (`flomation-${COMPONENT_NAME}.service`)

Generated from: `templates/systemd/service.template`

**Metadata Usage:**
- `Description=` → `package.summary`
- `WorkingDirectory=` → `/opt/flomation/${component_name}`
- `ExecStart=` → `/opt/flomation/${component_name}/${component_name}`
- Log paths → `/opt/flomation/${component_name}/logs/${component_name}.log`

### 3. Debian Package Files (`debian/`)

Generated from: `templates/apt/debian/`

**Metadata Usage:**

#### `debian/control`
- `Source:` → `package.name`
- `Package:` → `package.name`
- `Description:` → `package.summary` and `package.description`
- `Homepage:` → `package.url`
- `Maintainer:` → `maintainer.name <maintainer.email>`

#### `debian/changelog`
- Package name → `package.name`
- Maintainer → `maintainer.name <maintainer.email>`

#### `debian/rules`
- Build paths → Uses `package.name` and `package.component_name`

#### `debian/preinst`, `debian/postinst`, `debian/prerm`, `debian/postrm`
- Service name → `package.name`
- Installation paths → `/opt/flomation/${component_name}`

#### `debian/${PACKAGE_NAME}.install`
- Generated filename → `package.name`
- Service file → `flomation-${component_name}.service`

## Build Process

### 1. Metadata Injection

```bash
./scripts/inject-metadata.sh
```

This generates:
- `${PACKAGE_NAME}.spec` (e.g., `flomation-api.spec`)
- `flomation-${COMPONENT_NAME}.service` (e.g., `flomation-api.service`)
- `debian/` directory with all package files

### 2. CI Pipeline

The GitLab CI pipeline automatically:

1. **inject-metadata** stage:
   - Runs `scripts/inject-metadata.sh`
   - Exports `PACKAGE_NAME` variable
   - Uploads generated files as artifacts

2. **build** stage:
   - Builds RPM packages for EL8 and EL9
   - Builds DEB package
   - Uses artifacts from inject-metadata stage

3. **publish** stages:
   - Publishes packages to S3-based repositories

## Directory Structure

```
.
├── project-metadata.json          # Source of truth for all metadata
├── scripts/
│   └── inject-metadata.sh         # Metadata injection script
├── templates/
│   ├── apt/
│   │   └── debian/                # Debian package templates
│   ├── yum/
│   │   └── template.spec          # RPM spec template
│   └── systemd/
│       └── service.template       # Systemd service template
├── flomation-api.spec             # Generated RPM spec (not in git)
├── flomation-api.service          # Generated service file (not in git)
└── debian/                        # Generated debian package dir (not in git)
```

## Template Placeholders

Templates use `{{PLACEHOLDER}}` syntax for variable substitution:

| Placeholder | Replaced With |
|-------------|---------------|
| `{{PACKAGE_NAME}}` | `package.name` |
| `{{COMPONENT_NAME}}` | `package.component_name` |
| `{{PACKAGE_SUMMARY}}` | `package.summary` |
| `{{PACKAGE_DESCRIPTION}}` | `package.description` |
| `{{PACKAGE_LICENSE}}` | `package.license` |
| `{{PACKAGE_URL}}` | `package.url` |
| `{{MAINTAINER_NAME}}` | `maintainer.name` |
| `{{MAINTAINER_EMAIL}}` | `maintainer.email` |
| `{{ARCH_RPM}}` | `architecture.rpm` |

## Updating Package Metadata

To update package information:

1. Edit `project-metadata.json`
2. Run `./scripts/inject-metadata.sh` to regenerate files
3. Commit changes to `project-metadata.json` and `templates/` (do not commit generated files)
4. CI will automatically generate and build packages with new metadata

## Notes

- Generated files (`*.spec`, `*.service`, `debian/`) are not tracked in git
- Template files in `templates/` contain ShellCheck disable directives due to placeholder syntax
- Both RPM and DEB packages use the same systemd service template for consistency
- The `component_name` should be short (e.g., "api") as it's used in filesystem paths
- The `package.name` should follow distribution naming conventions (e.g., "flomation-api")
