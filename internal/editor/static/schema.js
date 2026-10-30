// Generated from internal/pkgspec (Describe) and internal/editor/help.yaml: don't edit.
// go test ./internal/editor -run TestSchemaFile -update-editor
(globalThis.RoostackEditor = globalThis.RoostackEditor || {}).schema = {
 "schema": "roostack/v1alpha1",
 "fields": {
  "type": "object",
  "fields": [
   {
    "name": "schema",
    "type": "string"
   },
   {
    "name": "name",
    "type": "string"
   },
   {
    "name": "title",
    "type": "string"
   },
   {
    "name": "description",
    "type": "string"
   },
   {
    "name": "version",
    "type": "string"
   },
   {
    "name": "about",
    "type": "object",
    "fields": [
     {
      "name": "maker",
      "type": "string"
     },
     {
      "name": "website",
      "type": "string"
     },
     {
      "name": "source",
      "type": "string"
     },
     {
      "name": "licence",
      "type": "string"
     },
     {
      "name": "support",
      "type": "string"
     },
     {
      "name": "releases",
      "type": "string"
     },
     {
      "name": "icon",
      "type": "string"
     },
     {
      "name": "categories",
      "type": "list",
      "item": {
       "type": "string"
      }
     },
     {
      "name": "keywords",
      "type": "list",
      "item": {
       "type": "string"
      }
     }
    ]
   },
   {
    "name": "requires",
    "type": "object",
    "fields": [
     {
      "name": "roostack",
      "type": "string"
     }
    ]
   },
   {
    "name": "built_with",
    "type": "list",
    "item": {
     "type": "string"
    }
   },
   {
    "name": "upgrade_notes",
    "type": "list",
    "item": {
     "type": "object",
     "fields": [
      {
       "name": "version",
       "type": "string"
      },
      {
       "name": "note",
       "type": "string"
      }
     ]
    }
   },
   {
    "name": "settings",
    "type": "list",
    "item": {
     "type": "object",
     "fields": [
      {
       "name": "key",
       "type": "string"
      },
      {
       "name": "title",
       "type": "string"
      },
      {
       "name": "description",
       "type": "string"
      },
      {
       "name": "type",
       "type": "string"
      },
      {
       "name": "default",
       "type": "any"
      },
      {
       "name": "generate",
       "type": "bool"
      },
      {
       "name": "required",
       "type": "bool"
      },
      {
       "name": "min_length",
       "type": "int"
      },
      {
       "name": "install_only",
       "type": "bool"
      },
      {
       "name": "reveal",
       "type": "bool"
      },
      {
       "name": "install_value",
       "type": "any"
      },
      {
       "name": "from_hook",
       "type": "bool"
      }
     ]
    }
   },
   {
    "name": "containers",
    "type": "map",
    "item": {
     "type": "object",
     "ptr": true,
     "fields": [
      {
       "name": "image",
       "type": "string"
      },
      {
       "name": "command",
       "type": "list",
       "item": {
        "type": "string"
       }
      },
      {
       "name": "env",
       "type": "map",
       "item": {
        "type": "string"
       }
      },
      {
       "name": "secrets",
       "type": "map",
       "item": {
        "type": "string"
       }
      },
      {
       "name": "mounts",
       "type": "list",
       "item": {
        "type": "object",
        "fields": [
         {
          "name": "storage",
          "type": "string"
         },
         {
          "name": "libraries",
          "type": "bool"
         },
         {
          "name": "path",
          "type": "string"
         },
         {
          "name": "readonly",
          "type": "bool"
         }
        ]
       }
      },
      {
       "name": "devices",
       "type": "list",
       "item": {
        "type": "object",
        "fields": [
         {
          "name": "gpu",
          "type": "string"
         },
         {
          "name": "when",
          "type": "string"
         }
        ]
       }
      },
      {
       "name": "health",
       "type": "object",
       "ptr": true,
       "fields": [
        {
         "name": "http",
         "type": "object",
         "ptr": true,
         "fields": [
          {
           "name": "port",
           "type": "int"
          },
          {
           "name": "path",
           "type": "string"
          }
         ]
        },
        {
         "name": "cmd",
         "type": "string"
        }
       ]
      },
      {
       "name": "depends_on",
       "type": "list",
       "item": {
        "type": "string"
       }
      },
      {
       "name": "when",
       "type": "string"
      },
      {
       "name": "memory",
       "type": "string"
      },
      {
       "name": "quadlet",
       "type": "quadlet"
      }
     ]
    }
   },
   {
    "name": "storage",
    "type": "map",
    "item": {
     "type": "object",
     "ptr": true,
     "fields": [
      {
       "name": "title",
       "type": "string"
      },
      {
       "name": "class",
       "type": "string"
      },
      {
       "name": "location",
       "type": "string"
      }
     ]
    }
   },
   {
    "name": "databases",
    "type": "map",
    "item": {
     "type": "object",
     "ptr": true,
     "fields": [
      {
       "name": "type",
       "type": "string"
      },
      {
       "name": "path",
       "type": "string"
      },
      {
       "name": "image",
       "type": "string"
      },
      {
       "name": "backup",
       "type": "bool",
       "ptr": true
      },
      {
       "name": "memory",
       "type": "string"
      },
      {
       "name": "schemas",
       "type": "list",
       "item": {
        "type": "string"
       }
      },
      {
       "name": "quadlet",
       "type": "quadlet"
      }
     ]
    }
   },
   {
    "name": "routes",
    "type": "map",
    "item": {
     "type": "object",
     "ptr": true,
     "fields": [
      {
       "name": "container",
       "type": "string"
      },
      {
       "name": "port",
       "type": "int"
      },
      {
       "name": "subdomain",
       "type": "string"
      },
      {
       "name": "exposure",
       "type": "string"
      },
      {
       "name": "max_body_size",
       "type": "string"
      },
      {
       "name": "client_ip_header",
       "type": "string"
      },
      {
       "name": "monitor",
       "type": "object",
       "ptr": true,
       "fields": [
        {
         "name": "path",
         "type": "string"
        },
        {
         "name": "statuses",
         "type": "list",
         "item": {
          "type": "int"
         }
        }
       ]
      },
      {
       "name": "public",
       "type": "object",
       "fields": [
        {
         "name": "unsafe",
         "type": "string"
        },
        {
         "name": "private_paths",
         "type": "list",
         "item": {
          "type": "object",
          "fields": [
           {
            "name": "path",
            "type": "string"
           },
           {
            "name": "reason",
            "type": "string"
           }
          ]
         }
        },
        {
         "name": "login_paths",
         "type": "list",
         "item": {
          "type": "string"
         }
        }
       ]
      }
     ]
    }
   },
   {
    "name": "ports",
    "type": "map",
    "item": {
     "type": "object",
     "ptr": true,
     "fields": [
      {
       "name": "title",
       "type": "string"
      },
      {
       "name": "description",
       "type": "string"
      },
      {
       "name": "container",
       "type": "string"
      },
      {
       "name": "port",
       "type": "int"
      },
      {
       "name": "protocol",
       "type": "string"
      },
      {
       "name": "host_port",
       "type": "int"
      },
      {
       "name": "enabled",
       "type": "bool"
      },
      {
       "name": "exposure",
       "type": "string"
      }
     ]
    }
   },
   {
    "name": "hooks",
    "type": "list",
    "item": {
     "type": "object",
     "ptr": true,
     "fields": [
      {
       "name": "on",
       "type": "string"
      },
      {
       "name": "name",
       "type": "string"
      },
      {
       "name": "title",
       "type": "string"
      },
      {
       "name": "description",
       "type": "string"
      },
      {
       "name": "container",
       "type": "string"
      },
      {
       "name": "command",
       "type": "list",
       "item": {
        "type": "string"
       }
      },
      {
       "name": "env",
       "type": "map",
       "item": {
        "type": "string"
       }
      },
      {
       "name": "secrets",
       "type": "map",
       "item": {
        "type": "string"
       }
      },
      {
       "name": "inputs",
       "type": "list",
       "item": {
        "type": "object",
        "fields": [
         {
          "name": "key",
          "type": "string"
         },
         {
          "name": "title",
          "type": "string"
         },
         {
          "name": "description",
          "type": "string"
         },
         {
          "name": "type",
          "type": "string"
         },
         {
          "name": "default",
          "type": "any"
         },
         {
          "name": "generate",
          "type": "bool"
         },
         {
          "name": "required",
          "type": "bool"
         },
         {
          "name": "min_length",
          "type": "int"
         },
         {
          "name": "install_only",
          "type": "bool"
         },
         {
          "name": "reveal",
          "type": "bool"
         },
         {
          "name": "install_value",
          "type": "any"
         },
         {
          "name": "from_hook",
          "type": "bool"
         }
        ]
       }
      },
      {
       "name": "saves",
       "type": "string"
      }
     ]
    }
   },
   {
    "name": "links",
    "type": "list",
    "item": {
     "type": "object",
     "fields": [
      {
       "name": "title",
       "type": "string"
      },
      {
       "name": "description",
       "type": "string"
      },
      {
       "name": "route",
       "type": "string"
      },
      {
       "name": "path",
       "type": "string"
      }
     ]
    }
   },
   {
    "name": "sso",
    "type": "object",
    "ptr": true,
    "fields": [
     {
      "name": "oidc",
      "type": "object",
      "ptr": true,
      "fields": [
       {
        "name": "route",
        "type": "string"
       },
       {
        "name": "redirect_paths",
        "type": "list",
        "item": {
         "type": "string"
        }
       },
       {
        "name": "redirect_uris",
        "type": "list",
        "item": {
         "type": "string"
        }
       },
       {
        "name": "groups",
        "type": "object",
        "ptr": true,
        "fields": [
         {
          "name": "users",
          "type": "string"
         },
         {
          "name": "admins",
          "type": "string"
         }
        ]
       },
       {
        "name": "admins",
        "type": "bool"
       },
       {
        "name": "existing_accounts",
        "type": "string"
       }
      ]
     },
     {
      "name": "forward_auth",
      "type": "object",
      "ptr": true,
      "fields": [
       {
        "name": "route",
        "type": "string"
       },
       {
        "name": "bypass_paths",
        "type": "list",
        "item": {
         "type": "object",
         "fields": [
          {
           "name": "path",
           "type": "string"
          },
          {
           "name": "reason",
           "type": "string"
          }
         ]
        }
       }
      ]
     },
     {
      "name": "env",
      "type": "map",
      "item": {
       "type": "map",
       "item": {
        "type": "string"
       }
      }
     },
     {
      "name": "secrets",
      "type": "map",
      "item": {
       "type": "map",
       "item": {
        "type": "string"
       }
      }
     },
     {
      "name": "secret_env",
      "type": "map",
      "item": {
       "type": "map",
       "item": {
        "type": "string"
       }
      }
     },
     {
      "name": "note",
      "type": "string"
     }
    ]
   }
  ]
 },
 "patterns": {
  "builtWith": "^[a-z][a-z0-9-]{0,31}$",
  "clientIPHeader": "^[Xx]-[A-Za-z0-9-]+$",
  "envName": "^[A-Za-z_][A-Za-z0-9_]*$",
  "icon": "^[A-Za-z0-9][A-Za-z0-9._-]*\\.png$",
  "memory": "^([0-9]+(?:\\.[0-9]+)?) ?(MB|GB)$",
  "name": "^[a-z][a-z0-9-]{0,30}$",
  "path": "^(/[A-Za-z0-9._:-]+)+$",
  "pinned": "@sha256:[0-9a-f]{64}$",
  "quadletKey": "^[A-Za-z][A-Za-z0-9]*$",
  "roostack": "^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)(\\.(0|[1-9][0-9]*))?$",
  "schema": "^[a-z][a-z0-9_]{0,30}$",
  "settingKey": "^[a-z][a-z0-9_]{0,62}$",
  "spdxID": "^[A-Za-z0-9][A-Za-z0-9.-]*\\+?$",
  "ssoAppScheme": "^[a-z][a-z0-9+.-]*:/[^\\s]*$",
  "ssoGroupName": "^[a-z][a-z0-9_-]{0,31}$",
  "ssoRedirectPath": "^(/([A-Za-z0-9._:~-]|%[0-9A-F]{2})*)+$",
  "subdomain": "^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$",
  "version": "^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$"
 },
 "lists": {
  "classes": [
   "critical",
   "replaceable"
  ],
  "databaseTypes": [
   "sqlite",
   "postgres",
   "mysql",
   "mariadb",
   "redis"
  ],
  "deprecatedLicences": [
   "GPL-1.0",
   "GPL-2.0",
   "GPL-3.0",
   "LGPL-2.0",
   "LGPL-2.1",
   "LGPL-3.0",
   "AGPL-1.0",
   "AGPL-3.0",
   "GFDL-1.1",
   "GFDL-1.2",
   "GFDL-1.3"
  ],
  "exposures": [
   "private",
   "public"
  ],
  "gpuKinds": [
   "intel"
  ],
  "hookEvents": [
   "install",
   "action",
   "sso"
  ],
  "locations": [
   "service",
   "media"
  ],
  "quadletSections": [
   "Unit",
   "Container",
   "Service",
   "Install"
  ],
  "settingTypes": [
   "bool",
   "string",
   "int",
   "secret"
  ],
  "ssoClientSecret": [
   "sso.client_secret"
  ],
  "ssoExisting": [
   "email",
   "username",
   "username_or_email",
   "separate"
  ]
 },
 "categories": [
  {
   "key": "photos",
   "title": "Photos and videos"
  },
  {
   "key": "media",
   "title": "Films, TV and music"
  },
  {
   "key": "documents",
   "title": "Documents and notes"
  },
  {
   "key": "productivity",
   "title": "Tasks and planning"
  },
  {
   "key": "home",
   "title": "Smart home and cameras"
  },
  {
   "key": "passwords",
   "title": "Passwords and security"
  },
  {
   "key": "development",
   "title": "Developer tools"
  },
  {
   "key": "publishing",
   "title": "Websites and blogs"
  },
  {
   "key": "network",
   "title": "Network"
  }
 ],
 "limits": {
  "iconMaxBytes": 100000,
  "iconMinSize": 256,
  "maxBuiltWith": 20,
  "maxCategories": 3,
  "maxKeywordBytes": 40,
  "maxKeywords": 10,
  "maxMakerBytes": 100,
  "maxMemory": 1000000000000,
  "maxStorageTitle": 60,
  "minHostPort": 1024,
  "minMemory": 1000000
 },
 "words": {
  "defaultAuto": "auto",
  "licenceClosed": "closed",
  "releasesPlaceholder": "{version}"
 },
 "databaseMemory": {
  "mariadb": 150000000,
  "mysql": 500000000,
  "postgres": 200000000,
  "redis": 50000000
 },
 "quadlet": {
  "allowed": {
   "Container": [
    "AddHost",
    "DNS",
    "DNSOption",
    "DNSSearch",
    "DropCapability",
    "Entrypoint",
    "Environment",
    "Exec",
    "ExposeHostPort",
    "HostName",
    "IP",
    "IP6",
    "Mask",
    "NetworkAlias",
    "NoNewPrivileges",
    "Notify",
    "PidsLimit",
    "Pull",
    "ReadOnly",
    "ReadOnlyTmpfs",
    "RunInit",
    "ShmSize",
    "StopSignal",
    "StopTimeout",
    "Sysctl",
    "Timezone",
    "Tmpfs",
    "Ulimit",
    "WorkingDir",
    "HealthCmd",
    "HealthInterval",
    "HealthMaxLogCount",
    "HealthMaxLogSize",
    "HealthOnFailure",
    "HealthRetries",
    "HealthStartPeriod",
    "HealthStartupCmd",
    "HealthStartupInterval",
    "HealthStartupRetries",
    "HealthStartupSuccess",
    "HealthStartupTimeout",
    "HealthTimeout",
    "Volume",
    "Mount",
    "User",
    "Group",
    "UserNS",
    "Label",
    "LogOpt",
    "GroupAdd",
    "HealthLogDestination"
   ],
   "Service": [
    "Restart",
    "RestartSec",
    "RestartSteps",
    "RestartMaxDelaySec",
    "RestartPreventExitStatus",
    "RestartForceExitStatus",
    "SuccessExitStatus",
    "TimeoutSec",
    "TimeoutStartSec",
    "TimeoutStopSec",
    "TimeoutAbortSec",
    "RuntimeMaxSec",
    "WatchdogSec",
    "CPUQuota",
    "CPUWeight",
    "IOWeight",
    "MemoryHigh",
    "MemoryMax",
    "MemorySwapMax",
    "TasksMax",
    "LimitNOFILE",
    "LimitNPROC",
    "Nice",
    "OOMPolicy",
    "OOMScoreAdjust"
   ],
   "Unit": [
    "Documentation",
    "StartLimitIntervalSec",
    "StartLimitBurst"
   ]
  },
  "rules": [
   {
    "section": "Container",
    "keys": [
     "Volume"
    ],
    "denies": "volumeHasSource",
    "instead": "a folder from the server or a named volume is outside the service and its backups; use storage: and mounts: (an anonymous Volume=/path is allowed)"
   },
   {
    "section": "Container",
    "keys": [
     "Mount"
    ],
    "denies": "notTmpfsMount",
    "instead": "only type=tmpfs is allowed, one per line, without quotes or backslashes; use storage: and mounts: for the service's folders"
   },
   {
    "section": "Container",
    "keys": [
     "Rootfs"
    ],
    "instead": "it runs the container from a folder on the server; use the container's image:"
   },
   {
    "section": "Container",
    "keys": [
     "EnvironmentFile",
     "EnvironmentHost",
     "ContainersConfModule",
     "SeccompProfile"
    ],
    "instead": "it reads or writes files on the server, outside the service; use storage:, env:, and secrets:"
   },
   {
    "section": "Container",
    "keys": [
     "LogOpt"
    ],
    "denies": "logPath",
    "instead": "a log path writes to a file on the server; Roostack keeps the container's log"
   },
   {
    "section": "Container",
    "keys": [
     "HealthLogDestination"
    ],
    "denies": "healthLogFolder",
    "instead": "a folder for the health log is on the server; use local or events_logger"
   },
   {
    "section": "Container",
    "keys": [
     "PodmanArgs",
     "GlobalArgs"
    ],
    "instead": "it can pass anything to Podman; something the package format can't say needs a new field"
   },
   {
    "section": "Container",
    "keys": [
     "AddCapability"
    ],
    "instead": "containers keep Podman's default capabilities"
   },
   {
    "section": "Container",
    "keys": [
     "SecurityLabel*",
     "Unmask",
     "Annotation"
    ],
    "instead": "Roostack keeps Podman's security settings; annotations can change them too"
   },
   {
    "section": "Container",
    "keys": [
     "Network",
     "Pod"
    ],
    "instead": "Roostack puts each service on its own network"
   },
   {
    "section": "Container",
    "keys": [
     "PublishPort"
    ],
    "instead": "use routes: for web addresses and ports: for other connections"
   },
   {
    "section": "Container",
    "keys": [
     "AddDevice"
    ],
    "instead": "use devices: (e.g. gpu: intel)"
   },
   {
    "section": "Container",
    "keys": [
     "GroupAdd"
    ],
    "denies": "keepGroups",
    "instead": "keep-groups gives the container the server's groups; use devices: for a GPU"
   },
   {
    "section": "Container",
    "keys": [
     "User",
     "Group"
    ],
    "denies": "rootUser",
    "instead": "run as the image's own user, with UserNS=keep-id:uid=…,gid=… if its files need that owner"
   },
   {
    "section": "Container",
    "keys": [
     "UIDMap",
     "GIDMap",
     "SubUIDMap",
     "SubGIDMap"
    ],
    "instead": "use UserNS=keep-id:uid=…,gid=… if the service's files need a particular owner"
   },
   {
    "section": "Container",
    "keys": [
     "UserNS"
    ],
    "denies": "sharedUserNS",
    "instead": "only keep-id, auto, nomap, or private: the others share a user namespace beyond the service; use UserNS=keep-id:uid=…,gid=… if the service's files need a particular owner"
   },
   {
    "section": "Container",
    "keys": [
     "Image",
     "AutoUpdate"
    ],
    "instead": "it would replace the pinned image; use image:"
   },
   {
    "section": "Container",
    "keys": [
     "Secret"
    ],
    "instead": "it could read another service's secrets; use secrets:"
   },
   {
    "section": "Container",
    "keys": [
     "ContainerName",
     "ServiceName"
    ],
    "instead": "Roostack names the containers and their units"
   },
   {
    "section": "Container",
    "keys": [
     "Label"
    ],
    "denies": "roostackLabel",
    "instead": "Roostack finds a service's containers by its own io.roostack labels (and write labels without backslashes)"
   },
   {
    "section": "Service",
    "keys": [
     "Exec*"
    ],
    "instead": "it runs a command on the server; commands run inside the container (command: or hooks:)"
   },
   {
    "section": "Service",
    "keys": [
     "Environment*",
     "PassEnvironment",
     "UnsetEnvironment"
    ],
    "instead": "it sets Podman's own environment, which can point it at other files; use env: for the container's"
   },
   {
    "section": "Service",
    "keys": [
     "Standard*",
     "TTY*",
     "PIDFile",
     "*Path*",
     "*Director*",
     "*Credential*",
     "*Image*",
     "TemporaryFileSystem",
     "Root*"
    ],
    "instead": "it reads or writes files on the server, outside the service; use storage:, env:, and secrets:"
   },
   {
    "section": "Service",
    "keys": [
     "User",
     "Group",
     "SupplementaryGroups",
     "DynamicUser",
     "PAMName",
     "KeyringMode"
    ],
    "instead": "the roostack user runs every service; use UserNS=keep-id:uid=…,gid=… for the container's user"
   },
   {
    "section": "Service",
    "keys": [
     "Type",
     "NotifyAccess",
     "Delegate*",
     "Slice",
     "Sockets",
     "BusName",
     "LogNamespace",
     "SELinuxContext*",
     "AppArmorProfile",
     "SmackProcessLabel"
    ],
    "instead": "it changes how Podman or systemd runs the container, beyond the service; something the package format can't say needs a new field"
   },
   {
    "section": "Unit",
    "keys": [
     "Exec*"
    ],
    "instead": "it runs a command on the server; commands run inside the container (command: or hooks:)"
   },
   {
    "section": "Unit",
    "keys": [
     "Requires",
     "Requisite",
     "Wants",
     "BindsTo",
     "PartOf",
     "Upholds",
     "Conflicts",
     "Before",
     "After",
     "OnFailure",
     "OnSuccess",
     "PropagatesReloadTo",
     "ReloadPropagatedFrom",
     "PropagatesStopTo",
     "StopPropagatedFrom",
     "JoinsNamespaceOf",
     "RequiresMountsFor",
     "WantsMountsFor",
     "SourcePath"
    ],
    "instead": "it ties the service to other units; Roostack writes it itself, from what the package declares (depends_on:)"
   },
   {
    "section": "Unit",
    "keys": [
     "*Action*",
     "RebootArgument"
    ],
    "instead": "it acts beyond the service, such as stopping every service of the roostack user"
   },
   {
    "section": "Install",
    "keys": [
     "*"
    ],
    "instead": "it ties the service to other units or names it after one; Roostack writes it itself, from what the package declares"
   }
  ]
 },
 "help": {
  "about": {
   "label": "The project",
   "help": "Facts about the project the package runs, shown in the catalog and on roostack.cloud/services, and checked when the package is reviewed. Registry packages need most of them; one for your own lab may leave them out.",
   "link": "packages/write#the-project"
  },
  "about.categories": {
   "label": "Categories",
   "help": "One to three, for the catalog's filters.",
   "link": "reference/packages#the-parts"
  },
  "about.icon": {
   "label": "Logo",
   "help": "The project's logo, a PNG file beside the package file (icon.png in the registry), square, at least 256×256 pixels and at most 100 KB.",
   "example": "icon.png",
   "link": "packages/write#the-project"
  },
  "about.keywords": {
   "label": "Keywords",
   "help": "Up to ten words or short phrases people might search for, beyond the app's name.",
   "example": "notes, wiki, markdown",
   "link": "reference/packages#the-parts"
  },
  "about.licence": {
   "label": "Licence",
   "help": "The app's licence as an SPDX identifier, taken from the project's own licence file or notice. For the GNU licences, say -only or -or-later as the notice does. For an app whose code isn't published, closed.",
   "example": "AGPL-3.0-only",
   "link": "packages/write#the-project"
  },
  "about.maker": {
   "label": "Who makes it",
   "help": "Who makes the app, in a few words, so that the catalog credits them.",
   "example": "a volunteer community",
   "link": "reference/packages#the-parts"
  },
  "about.releases": {
   "label": "What's new",
   "help": "Where the project says what's new in each version, with {version} where the app's version goes (without -roostack.N), or one page for every version. A service's update will link to it.",
   "example": "https://github.com/example/myapp/releases/tag/v{version}",
   "link": "packages/write#the-project"
  },
  "about.source": {
   "label": "Source code",
   "help": "Where the app's code is published, an https:// address. An app whose code isn't published (licence closed) has none.",
   "example": "https://github.com/example/myapp",
   "link": "reference/packages#the-parts"
  },
  "about.support": {
   "label": "Donations",
   "help": "The project's own page for donating or sponsoring, if it has one.",
   "example": "https://opencollective.com/myapp",
   "link": "reference/packages#the-parts"
  },
  "about.website": {
   "label": "Website",
   "help": "The project's own site, an https:// address. Leave it out if the project has none beyond its code.",
   "example": "https://myapp.example",
   "link": "reference/packages#the-parts"
  },
  "built_with": {
   "label": "Built with",
   "help": "What the app is built on, as short names such as php, node, laravel or log4j, not every library it uses. If that thing has a security problem, the Owner will be able to pause public access to every service built with it at once. Roostack will add the databases it runs for the app by itself.",
   "example": "php, laravel",
   "link": "reference/packages#the-parts"
  },
  "containers": {
   "label": "Containers",
   "help": "The app's containers, each from an image the project publishes itself. Databases aren't containers here: declare them under databases, and Roostack will run them.",
   "link": "packages/write#containers-and-images"
  },
  "containers.*.command": {
   "label": "Command",
   "help": "What the container runs, if not the image's own command, one argument per item. Templates work here.",
   "link": "reference/packages#the-parts"
  },
  "containers.*.depends_on": {
   "label": "Starts after",
   "help": "What has to start first, such as databases.main or containers.worker.",
   "example": "databases.main",
   "link": "packages/write#containers-and-images"
  },
  "containers.*.devices": {
   "label": "Devices",
   "help": "Hardware the container asks for, such as a GPU for transcoding or recognising faces.",
   "link": "reference/packages#the-parts"
  },
  "containers.*.devices[].gpu": {
   "label": "GPU",
   "help": "The kind of GPU; intel for now.",
   "example": "intel",
   "link": "reference/packages#the-parts"
  },
  "containers.*.devices[].when": {
   "label": "When",
   "help": "A template that renders true or false; the device will be asked for only when true.",
   "example": "{{ .Settings.hardware_transcoding }}",
   "link": "reference/packages#templates"
  },
  "containers.*.env": {
   "label": "Environment",
   "help": "Settings as environment variables, as the app's documentation lists them. Values can be templates, such as {{ .Routes.web.URL }} for the app's own address.",
   "link": "packages/write#containers-and-images"
  },
  "containers.*.health": {
   "label": "Health check",
   "help": "How Roostack will tell the container is working. Set http or cmd, not both.",
   "link": "packages/write#containers-and-images"
  },
  "containers.*.health.cmd": {
   "label": "Command",
   "help": "A command run inside the container instead; it works when it exits with 0.",
   "example": "pg_isready",
   "link": "reference/packages#the-parts"
  },
  "containers.*.health.http.path": {
   "label": "Path",
   "help": "The address to ask; any answer below 500 means it's working. Many apps have one, such as /health or /api/status.",
   "example": "/health",
   "link": "packages/write#containers-and-images"
  },
  "containers.*.health.http.port": {
   "label": "Port",
   "help": "The port inside the container to ask.",
   "example": "8080",
   "link": "packages/write#containers-and-images"
  },
  "containers.*.image": {
   "label": "Image",
   "help": "The project's own image, pinned to its tag and its multi-architecture digest (name:tag@sha256:…) so that every lab runs the image you tried.",
   "example": "docker.io/example/myapp:2.4.1@sha256:…",
   "link": "packages/write#containers-and-images"
  },
  "containers.*.memory": {
   "label": "Memory",
   "help": "What it typically uses, such as 300 MB or 1.5 GB (1 GB is 1000 MB), for the summary shown before installing. It's an estimate, not a limit, so measure it once it's running.",
   "example": "300 MB",
   "link": "packages/try#measure-its-memory"
  },
  "containers.*.mounts": {
   "label": "Mounts",
   "help": "The app's data folders, each a storage entry mounted at a path, or the media libraries the Owner chose.",
   "link": "packages/write#data"
  },
  "containers.*.mounts[].libraries": {
   "label": "Media libraries",
   "help": "Mount the media libraries the Owner chose for the service instead, each in a folder of its name under the path, read-only and never backed up.",
   "link": "reference/packages#the-parts"
  },
  "containers.*.mounts[].path": {
   "label": "Path",
   "help": "Where it appears inside the container, from the app's documentation.",
   "example": "/data",
   "link": "packages/write#data"
  },
  "containers.*.mounts[].readonly": {
   "label": "Read-only",
   "help": "The container can read it but not change it.",
   "link": "reference/packages#the-parts"
  },
  "containers.*.mounts[].storage": {
   "label": "Storage",
   "help": "Which storage entry to mount.",
   "example": "data",
   "link": "packages/write#data"
  },
  "containers.*.quadlet": {
   "label": "Quadlet lines",
   "help": "Extra lines for the container's Quadlet file, by section, for what the format doesn't cover. Only the keys the package policy allows, such as UserNS=keep-id:uid=1000,gid=1000 for an app whose files need that owner.",
   "link": "reference/packages#what-a-package-may-not-do"
  },
  "containers.*.secrets": {
   "label": "Secrets",
   "help": "Passwords and keys as environment variables, each pointing at a secret setting, databases.\u003cname\u003e.password, or databases.\u003cname\u003e.url (Postgres's whole address). Roostack will keep them as Podman secrets, never in a file.",
   "link": "packages/write#containers-and-images"
  },
  "containers.*.when": {
   "label": "When",
   "help": "A template that renders true or false; Roostack will leave the container out unless it's true. It's for optional parts so that turned-off features don't use memory.",
   "example": "{{ .Settings.smart_search }}",
   "link": "reference/packages#templates"
  },
  "databases": {
   "label": "Databases",
   "help": "Databases Roostack will run, make passwords for, and back up consistently. Declare them here rather than running your own container.",
   "link": "packages/write#data"
  },
  "databases.*.backup": {
   "label": "Backed up",
   "help": "Leave it out to back the database up, or set it to false for a Redis used only as a queue or cache, which is safe to lose.",
   "link": "packages/write#data"
  },
  "databases.*.image": {
   "label": "Image",
   "help": "Another image instead of Roostack's own, pinned by digest, for an app that needs extensions (Immich's Postgres).",
   "link": "reference/packages#the-parts"
  },
  "databases.*.memory": {
   "label": "Memory",
   "help": "What it typically uses, if not Roostack's estimate (200 MB for Postgres, 500 MB for MySQL, 150 MB for MariaDB, 50 MB for Redis).",
   "example": "200 MB",
   "link": "reference/packages#the-parts"
  },
  "databases.*.path": {
   "label": "File",
   "help": "For sqlite, the file's place in a storage folder, as storage/file. Roostack will copy it safely before each backup.",
   "example": "data/db.sqlite3",
   "link": "packages/write#data"
  },
  "databases.*.quadlet": {
   "label": "Quadlet lines",
   "help": "Extra lines for the container's Quadlet file, by section, for what the format doesn't cover. Only the keys the package policy allows, such as UserNS=keep-id:uid=1000,gid=1000 for an app whose files need that owner.",
   "link": "reference/packages#what-a-package-may-not-do"
  },
  "databases.*.schemas": {
   "label": "More schemas",
   "help": "For mysql or mariadb, more schemas on the same server for the same user, for an app that wants several. Roostack will name each after the database, as \u003cdatabase\u003e_\u003cschema\u003e, make it, and back it up with the database; templates find its name with {{ .Databases.\u003cname\u003e.Schemas.\u003cschema\u003e }}.",
   "example": "[ccnet, seahub]",
   "link": "reference/packages#the-parts"
  },
  "databases.*.type": {
   "label": "Type",
   "help": "sqlite (a file in the app's storage), postgres, mysql, mariadb, or redis (a container of their own). Roostack runs Postgres 17, MySQL 8.4, MariaDB 11.8, and Valkey.",
   "link": "packages/write#data"
  },
  "description": {
   "label": "Description",
   "help": "What it does, in one line, for the list of services. Say what people get, not how it works.",
   "example": "Keeps your notes and finds them again",
   "link": "packages/write#name-and-version"
  },
  "hooks": {
   "label": "Setup and actions",
   "help": "Commands run inside a container, once after the first start (install), as a button on the service's page (action), or when single sign-on is turned on, changed, or off (sso). Values people type will reach them only as environment variables.",
   "link": "packages/write#settings-setup-and-links"
  },
  "hooks[].command": {
   "label": "Command",
   "help": "Run as it is, one argument per item, with no templates: settings and inputs will arrive as environment variables, so they can't change what a script does.",
   "link": "packages/write#settings-setup-and-links"
  },
  "hooks[].container": {
   "label": "Container",
   "help": "The container it runs in.",
   "link": "reference/packages#the-parts"
  },
  "hooks[].description": {
   "label": "Description",
   "help": "What it does, shown beside the button.",
   "link": "reference/packages#the-parts"
  },
  "hooks[].env": {
   "label": "Environment",
   "help": "Environment variables for the command; values can be templates, such as {{ .Settings.admin_username }} or an action's {{ .Inputs.username }}.",
   "link": "reference/packages#templates"
  },
  "hooks[].inputs": {
   "label": "Inputs",
   "help": "For an action, what to ask for when it runs: strings or secrets.",
   "link": "reference/packages#the-parts"
  },
  "hooks[].inputs[].default": {
   "label": "Default",
   "help": "Not for inputs.",
   "link": "reference/packages#the-parts"
  },
  "hooks[].inputs[].description": {
   "label": "Description",
   "help": "A sentence under the title, if the title doesn't say it all.",
   "link": "packages/write#settings-setup-and-links"
  },
  "hooks[].inputs[].from_hook": {
   "label": "Default",
   "help": "Not for inputs.",
   "link": "reference/packages#the-parts"
  },
  "hooks[].inputs[].generate": {
   "label": "Default",
   "help": "Not for inputs.",
   "link": "reference/packages#the-parts"
  },
  "hooks[].inputs[].install_only": {
   "label": "Default",
   "help": "Not for inputs.",
   "link": "reference/packages#the-parts"
  },
  "hooks[].inputs[].install_value": {
   "label": "Default",
   "help": "Not for inputs.",
   "link": "reference/packages#the-parts"
  },
  "hooks[].inputs[].key": {
   "label": "Key",
   "help": "Lowercase letters, digits, and underscores. Templates use it as {{ .Settings.key }}, and secrets: name it.",
   "example": "signups_allowed",
   "link": "packages/write#settings-setup-and-links"
  },
  "hooks[].inputs[].min_length": {
   "label": "Shortest",
   "help": "For a string or secret, the fewest characters accepted, such as 8 for a password.",
   "example": "8",
   "link": "reference/packages#the-parts"
  },
  "hooks[].inputs[].required": {
   "label": "Required",
   "help": "For a string: it can't be left empty.",
   "link": "reference/packages#the-parts"
  },
  "hooks[].inputs[].reveal": {
   "label": "Default",
   "help": "Not for inputs.",
   "link": "reference/packages#the-parts"
  },
  "hooks[].inputs[].title": {
   "label": "Title",
   "help": "The setting's name on the install form and the service's settings.",
   "example": "Allow anyone to create an account",
   "link": "packages/write#settings-setup-and-links"
  },
  "hooks[].inputs[].type": {
   "label": "Type",
   "help": "string or secret.",
   "link": "reference/packages#the-parts"
  },
  "hooks[].name": {
   "label": "Name",
   "help": "For an action, lowercase letters, digits, and dashes, used in its address.",
   "example": "reset-password",
   "link": "reference/packages#the-parts"
  },
  "hooks[].on": {
   "label": "When",
   "help": "install will run once after the first start and must be safe to run twice; action is a button on the service's page; sso will run when single sign-on is turned on, changed (the lab's address or name), or off, told which by ROOSTACK_SSO=on or off, and must be safe to run again.",
   "link": "packages/write#settings-setup-and-links"
  },
  "hooks[].saves": {
   "label": "Keeps what it prints",
   "help": "For an install hook or an action: a from_hook secret setting, which will become what the hook prints on standard output (one line, never shown or logged). Print everything else on standard error, the log.",
   "example": "settings_key",
   "link": "packages/write#single-sign-on"
  },
  "hooks[].secrets": {
   "label": "Secrets",
   "help": "Secret settings as environment variables, or for an action inputs.\u003ckey\u003e for a secret input. An sso hook of an OIDC app may also have sso.client_secret, which is empty while single sign-on is off.",
   "link": "reference/packages#the-parts"
  },
  "hooks[].title": {
   "label": "Title",
   "help": "For an action, the button's label.",
   "example": "Reset a password",
   "link": "reference/packages#the-parts"
  },
  "links": {
   "label": "Links into the app",
   "help": "The app's own settings pages, shown first under Settings on the service's page.",
   "link": "packages/write#settings-setup-and-links"
  },
  "links[].description": {
   "label": "Description",
   "help": "What's there, and who can use it.",
   "link": "reference/packages#the-parts"
  },
  "links[].path": {
   "label": "Path",
   "help": "The page's path, starting with a single /.",
   "example": "/admin",
   "link": "reference/packages#the-parts"
  },
  "links[].route": {
   "label": "Web address",
   "help": "Which of the package's web addresses it's on.",
   "example": "web",
   "link": "reference/packages#the-parts"
  },
  "links[].title": {
   "label": "Title",
   "help": "The link's text.",
   "example": "Site administration",
   "link": "reference/packages#the-parts"
  },
  "name": {
   "label": "Name",
   "help": "Lowercase letters, digits, and dashes, starting with a letter. It will name the service's folders, containers, and backups, so it can't change once people use it.",
   "example": "myapp",
   "link": "packages/write#name-and-version"
  },
  "ports": {
   "label": "Extra connections",
   "help": "Connections that aren't web pages, such as Git over SSH: a container's port published on the server, off unless the Admin turns it on.",
   "link": "reference/packages#the-parts"
  },
  "ports.*.container": {
   "label": "Container",
   "help": "The container whose port it is.",
   "link": "reference/packages#the-parts"
  },
  "ports.*.description": {
   "label": "Description",
   "help": "A sentence on what it's for.",
   "link": "reference/packages#the-parts"
  },
  "ports.*.enabled": {
   "label": "On by default",
   "help": "Turn it on when the service is installed. Most leave it off.",
   "link": "reference/packages#the-parts"
  },
  "ports.*.exposure": {
   "label": "Exposure",
   "help": "Always private in a package (home only); only the Owner can open a connection to the internet.",
   "link": "reference/packages#the-parts"
  },
  "ports.*.host_port": {
   "label": "Port on the server",
   "help": "The suggested port on the server, 1024 or above; the Admin can change it.",
   "example": "2222",
   "link": "reference/packages#the-parts"
  },
  "ports.*.port": {
   "label": "Port inside",
   "help": "The port inside the container.",
   "example": "22",
   "link": "reference/packages#the-parts"
  },
  "ports.*.protocol": {
   "label": "Protocol",
   "help": "tcp (the default) or udp.",
   "link": "reference/packages#the-parts"
  },
  "ports.*.title": {
   "label": "Title",
   "help": "What the connection is, as people see it.",
   "example": "Git over SSH",
   "link": "reference/packages#the-parts"
  },
  "requires": {
   "label": "What it needs",
   "link": "reference/packages#the-parts"
  },
  "requires.roostack": {
   "label": "Oldest Roostack",
   "help": "The oldest Roostack release the package works with, such as 0.6 or 0.6.1. The one you tried it on, unless you know better. A lab running an older one won't offer it.",
   "example": "0.6",
   "link": "packages/write#the-project"
  },
  "routes": {
   "label": "Web addresses",
   "help": "Where people reach the app. Every address will start private (home only) until the Owner makes it public, and the package says what's safe once it is.",
   "link": "packages/write#its-web-address"
  },
  "routes.*.client_ip_header": {
   "label": "Visitor's address header",
   "help": "For an app that reads the visitor's address from a header other than X-Forwarded-For, such as X-Real-IP. Roostack will set it, replacing any the visitor sent.",
   "example": "X-Real-IP",
   "link": "reference/packages#the-parts"
  },
  "routes.*.container": {
   "label": "Container",
   "help": "The container that answers.",
   "link": "packages/write#its-web-address"
  },
  "routes.*.exposure": {
   "label": "Exposure",
   "help": "Always private in a package; only the Owner can make an address public.",
   "link": "packages/write#its-web-address"
  },
  "routes.*.max_body_size": {
   "label": "Largest upload",
   "help": "The largest request the app accepts, for apps people upload large files to, such as 100MB.",
   "example": "100MB",
   "link": "reference/packages#the-parts"
  },
  "routes.*.monitor": {
   "label": "Monitoring",
   "help": "How monitoring will check the address, for an app whose usual answer doesn't say it works. Most packages leave it out.",
   "link": "servers/monitoring"
  },
  "routes.*.monitor.path": {
   "label": "Path",
   "help": "The address to ask instead of the health check's or /.",
   "example": "/api/health",
   "link": "servers/monitoring"
  },
  "routes.*.monitor.statuses": {
   "label": "Statuses",
   "help": "The HTTP statuses that count as up, instead of any below 500.",
   "example": "200",
   "link": "servers/monitoring"
  },
  "routes.*.port": {
   "label": "Port",
   "help": "The port inside the container, from the app's documentation (in a Compose file, the second number of 8080:80).",
   "example": "8080",
   "link": "packages/write#its-web-address"
  },
  "routes.*.public": {
   "label": "Once it's public",
   "help": "What's safe once the Owner makes the address public. A registry package names where people sign in, or says why the address shouldn't be public at all.",
   "link": "packages/write#its-web-address"
  },
  "routes.*.public.login_paths": {
   "label": "Where people sign in",
   "help": "The addresses that check a password. Outside visitors will get a strict limit on tries there so that nobody can guess passwords quickly.",
   "example": "/login",
   "link": "packages/write#its-web-address"
  },
  "routes.*.public.private_paths": {
   "label": "Home-only paths",
   "help": "Paths, and everything under them, that stay home-only on a public address, such as an admin page.",
   "link": "packages/write#its-web-address"
  },
  "routes.*.public.private_paths[].path": {
   "label": "Path",
   "help": "Plain, starting with /, with letters, digits, and - _ . : between the slashes, and no * or trailing /.",
   "example": "/admin",
   "link": "packages/write#its-web-address"
  },
  "routes.*.public.private_paths[].reason": {
   "label": "Why",
   "help": "Why it shouldn't be public, shown to the Owner if they open it anyway.",
   "example": "The app's admin pages, which change every account.",
   "link": "packages/write#its-web-address"
  },
  "routes.*.public.unsafe": {
   "label": "Not safe to make public",
   "help": "Why the whole address shouldn't be public, such as an API with no login. The Owner will see it as a warning and can still go ahead.",
   "link": "packages/write#its-web-address"
  },
  "routes.*.subdomain": {
   "label": "Subdomain",
   "help": "The default name before your lab's domain, such as notes for notes.smith.family. People can change it.",
   "example": "notes",
   "link": "packages/write#its-web-address"
  },
  "schema": {
   "label": "Schema",
   "help": "Which version of the package format this is. Always roostack/v1alpha1 for now.",
   "example": "roostack/v1alpha1",
   "link": "reference/packages#the-parts"
  },
  "settings": {
   "label": "Settings",
   "help": "What people choose, at install and later on the service's page. Most of an app's settings stay in the app; these are the ones it needs from the start, and secrets.",
   "link": "packages/write#settings-setup-and-links"
  },
  "settings[].default": {
   "label": "Default",
   "help": "The value until someone chooses another. Secrets have none (use generate). A bool can be auto, on when the server has a GPU.",
   "link": "reference/packages#the-parts"
  },
  "settings[].description": {
   "label": "Description",
   "help": "A sentence under the title, if the title doesn't say it all.",
   "link": "packages/write#settings-setup-and-links"
  },
  "settings[].from_hook": {
   "label": "Made by the app",
   "help": "For a secret the app makes, such as an API key for changing its own settings: a hook will print it and Roostack will keep it (the hook's saves). It's never shown and is used only by hooks, and it needs an action that can make it again.",
   "link": "packages/write#single-sign-on"
  },
  "settings[].generate": {
   "label": "Roostack makes it",
   "help": "For a secret: Roostack will create a random value, so nobody has to think of one.",
   "link": "packages/write#settings-setup-and-links"
  },
  "settings[].install_only": {
   "label": "Only at install",
   "help": "Asked for at install and used only by install hooks, such as the first administrator's password. Roostack will delete a secret like this once setup has run.",
   "link": "packages/write#settings-setup-and-links"
  },
  "settings[].install_value": {
   "label": "Value during install",
   "help": "Roostack will use it until the install hooks succeed, whatever was chosen, and then the chosen value. For an app that makes its first account only by signing up, sign-ups will be on while the hook is signing the Owner up, and then off. It needs an install hook.",
   "link": "packages/write#settings-setup-and-links"
  },
  "settings[].key": {
   "label": "Key",
   "help": "Lowercase letters, digits, and underscores. Templates use it as {{ .Settings.key }}, and secrets: name it.",
   "example": "signups_allowed",
   "link": "packages/write#settings-setup-and-links"
  },
  "settings[].min_length": {
   "label": "Shortest",
   "help": "For a string or secret, the fewest characters accepted, such as 8 for a password.",
   "example": "8",
   "link": "reference/packages#the-parts"
  },
  "settings[].required": {
   "label": "Required",
   "help": "For a string: it can't be left empty.",
   "link": "reference/packages#the-parts"
  },
  "settings[].reveal": {
   "label": "Admins can see it",
   "help": "For a secret that's kept: Admins can show it on the service's page (Roostack will log each view), such as a token for the app's own admin page.",
   "link": "reference/packages#the-parts"
  },
  "settings[].title": {
   "label": "Title",
   "help": "The setting's name on the install form and the service's settings.",
   "example": "Allow anyone to create an account",
   "link": "packages/write#settings-setup-and-links"
  },
  "settings[].type": {
   "label": "Type",
   "help": "bool (on or off), string, int (a whole number), or secret (a password or key: a Podman secret, never in a file).",
   "link": "reference/packages#the-parts"
  },
  "sso": {
   "label": "Single sign-on",
   "help": "How the app takes single sign-on, which the Owner turns on per service. Either oidc (the app will sign people in through OIDC itself) or forward_auth (everyone will sign in on the lab's sign-in page before a request reaches it), not both.",
   "link": "services/single-sign-on"
  },
  "sso.env": {
   "label": "Settings while it's on",
   "help": "Environment variables added to the named containers only while single sign-on is on (container, then variable), replacing their own value of the same name. Templates have .SSO, such as {{ .SSO.DiscoveryURL }} and {{ .SSO.ClientID }}.",
   "link": "reference/packages#the-parts"
  },
  "sso.forward_auth.bypass_paths": {
   "label": "Paths outside the wall",
   "help": "Addresses that stay reachable without signing in to the lab, each with why that's safe, such as an API the phone apps use with the app's own tokens, or share links.",
   "link": "reference/packages#the-parts"
  },
  "sso.forward_auth.bypass_paths[].path": {
   "label": "Path",
   "help": "A path that stays outside the wall, such as an API the phone apps use with the app's own tokens. Plain, starting with /, with letters, digits, and - _ . : between the slashes, and no * or trailing /.",
   "example": "/api",
   "link": "reference/packages#the-parts"
  },
  "sso.forward_auth.bypass_paths[].reason": {
   "label": "Why",
   "help": "Why it's safe without signing in to the lab, such as the app checking its own tokens there.",
   "example": "The phone apps' API, which checks the app's own tokens.",
   "link": "reference/packages#the-parts"
  },
  "sso.forward_auth.route": {
   "label": "Sign-in wall, in front of the address",
   "help": "The route that goes behind the wall. Its containers may read who someone is from the Remote-User, Remote-Email, Remote-Name, and Remote-Groups headers; Roostack will remove any a visitor sends.",
   "example": "web",
   "link": "reference/packages#the-parts"
  },
  "sso.note": {
   "label": "Note",
   "help": "What still uses the app's own sign-in (its phone apps, its API tokens), shown beside the switch on the service's page.",
   "example": "The phone apps still sign in with the app's own accounts.",
   "link": "reference/packages#the-parts"
  },
  "sso.oidc.admins": {
   "label": "Makes the lab's admins its admins",
   "help": "On if the app will make the lab's Owners and Admins its own admins, from the admins group in its groups claim (Mealie, Forgejo, Immich). The service's Single sign-on card will say so only then.",
   "link": "reference/packages#the-parts"
  },
  "sso.oidc.existing_accounts": {
   "label": "An account already in the app",
   "help": "What the app will do when someone who signs in already has an account in it: email (use the one with their email address), username (the one with their username), username_or_email (their username, or else their email address), or separate (make a new one beside it). Left out, the service's card will say nothing about those accounts.",
   "example": "email",
   "link": "reference/packages#the-parts"
  },
  "sso.oidc.groups": {
   "label": "Group names the app expects",
   "help": "The two groups the app is told about in its groups claim, renamed for an app that expects its own words: users (everyone with access) and admins (the lab's Owners and Admins). Immich and Audiobookshelf read user and admin.",
   "link": "reference/packages#the-parts"
  },
  "sso.oidc.groups.admins": {
   "label": "Group name for admins",
   "help": "The name of the group the lab's Owners and Admins are in too.",
   "example": "admin",
   "link": "reference/packages#the-parts"
  },
  "sso.oidc.groups.users": {
   "label": "Group name for everyone",
   "help": "The name of the group everyone with access is in.",
   "example": "user",
   "link": "reference/packages#the-parts"
  },
  "sso.oidc.redirect_paths": {
   "label": "OIDC callback paths",
   "help": "Where the sign-in page may send people back to on that address, exactly as the app's OIDC settings say (its callback path). A path may use .SSO, such as Forgejo's /user/oauth2/{{ pathescape .SSO.ProviderName }}/callback.",
   "example": "/auth/login",
   "link": "reference/packages#the-parts"
  },
  "sso.oidc.redirect_uris": {
   "label": "OIDC phone app addresses",
   "help": "The app's phone apps' own callback addresses, if they sign in through the lab too. Never http or https, which could be anyone's site.",
   "example": "app.immich:///oauth-callback",
   "link": "reference/packages#the-parts"
  },
  "sso.oidc.route": {
   "label": "OIDC, on the address",
   "help": "The route whose address the redirect paths are on.",
   "example": "web",
   "link": "reference/packages#the-parts"
  },
  "sso.secret_env": {
   "label": "Secret inside a value",
   "help": "For an app that takes its client secret inside a bigger value, such as a JSON list of providers: container, then variable, then a template using {{ .SSO.ClientSecret }} (with json for each value). Roostack will make the value as a Podman secret, never in a file it writes.",
   "link": "reference/packages#the-parts"
  },
  "sso.secrets": {
   "label": "Client secret",
   "help": "Gives a container the app's client secret, which Roostack will make: container, then variable, then sso.client_secret, such as OIDC_CLIENT_SECRET: sso.client_secret. OIDC apps only.",
   "link": "reference/packages#the-parts"
  },
  "storage": {
   "label": "Data",
   "help": "Every folder the app writes to that should survive a restart. Getting the classes right is the most important part of a package.",
   "link": "packages/write#data"
  },
  "storage.*.class": {
   "label": "Backed up",
   "help": "critical will be backed up; replaceable won't, because the app makes it again (caches, thumbnails, converted files). Anything people would miss is critical.",
   "link": "packages/write#data"
  },
  "storage.*.location": {
   "label": "Where",
   "help": "service (Roostack's service folder) or media (the large data disk, if there is one), for large data such as photos and documents.",
   "link": "reference/packages#the-parts"
  },
  "storage.*.title": {
   "label": "What it holds",
   "help": "A few words for people, for the summary before installing (\"Your notes and attachments\"). At most 60 characters.",
   "example": "Your notes and attachments",
   "link": "packages/write#data"
  },
  "title": {
   "label": "Title",
   "help": "The app's name as people know it, shown in the catalog and on the service's page.",
   "example": "My App",
   "link": "packages/write#name-and-version"
  },
  "upgrade_notes": {
   "label": "Upgrade notes",
   "help": "What to know before updating, shown before a service updates. Keep the notes still needed from earlier versions so that someone updating past several at once sees all of them.",
   "link": "reference/packages#the-parts"
  },
  "upgrade_notes[].note": {
   "label": "Note",
   "help": "What to know or do, in a sentence or two.",
   "example": "Version 2 will move sharing links to a new address, so send new ones to people you shared with.",
   "link": "reference/packages#the-parts"
  },
  "upgrade_notes[].version": {
   "label": "Arrived in",
   "help": "The package version the note arrived in.",
   "example": "2.0.0-roostack.1",
   "link": "reference/packages#the-parts"
  },
  "version": {
   "label": "Version",
   "help": "The app's own version, then -roostack. and the package's revision, starting at 1. A better package for the same app version is a new revision, in a new file.",
   "example": "2.4.1-roostack.1",
   "link": "packages/write#name-and-version"
  }
 },
 "valueTests": [
  "healthLogFolder",
  "keepGroups",
  "logPath",
  "notTmpfsMount",
  "roostackLabel",
  "rootUser",
  "sharedUserNS",
  "volumeHasSource"
 ]
};
