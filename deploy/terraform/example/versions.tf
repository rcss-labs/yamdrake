terraform {
  # 1.5.x is the oldest line this example is validated with; 1.x later releases work too.
  required_version = ">= 1.5.7, < 2.0.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.100.0"
    }
  }
}
