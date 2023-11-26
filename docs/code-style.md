# Barry's Code Style

Barry is an opinionated Terraform formatter with its own style which extends the
`terraform fmt` command. Barry aims for consistency, generality, readability,
and reducing git diffs. Similar language constructs are formatted with similar
rules. Style configuration options are deliberately limited and rarely added.

The rest of this document describes the current formatting style.

## Empty lines

Barry avoids spurious vertical whitespace by replacing all instances of two or
more consecutive empty lines with a single empty line.

Barry ensures that all top-level blocks are separated by a single empty line. It
also separates all nested blocks with a single empty line unless they are of the
same type.

```hcl
resource "aws_instance" "example" {
  ami           = "abc123"
  instance_type = "t2.micro"

  network_interface {
    example = "value"
  } # no empty line as blocks are of the same type
  network_interface {
    example = "value"
  }
}
```

## Arguments and blocks

Barry does not change the order of top-level blocks (`resource`, `data`,
`modules`, etc.) within the file.

Barry sorts all arguments and nested blocks alphabetically and then orders them
using the following rules.

When both arguments and blocks appear together inside a block body, all of the
arguments are placed together at the top and nested blocks are placed below
them. A single empty line separates the arguments from the blocks.

For blocks that contain both arguments and "meta-arguments" (as defined by the
Terraform language semantics), meta-arguments are placed first and are separated
from other arguments with a single empty line.

Meta-argument blocks are placed last and are separated from other blocks with a
single empty line.

```hcl
resource "aws_instance" "example" {
  count = 2 # meta-argument first

  ami           = "abc123"
  instance_type = "t2.micro"

  network_interface {
    # ...
  }
  network_interface { # multiple blocks of the same type not separated by empty line
    # ...
  }

  lifecycle { # meta-argument block last
    create_before_destroy = true
  }
}
```

For `module` blocks, Barry treats the `source` and `version` arguments
differently to other non-meta arguments and places them at the top of the block.
These are separated from other arguments by a single empty line.

```hcl
module "example" {
  source  = "GoogleCloudPlatform/cloud-run/google//examples/simple_cloud_run"
  version = "~> 0.10.0"

  for_each = var.example # meta-argument follows source/version
  
  project_id = "my-project-id"
}
```

## Comments

Barry does not format comment contents, however, it does replace the `//`
single-line comment syntax with the preferred `#` syntax.
