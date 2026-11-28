provider "aws" {
  region = var.region

  default_tags {
    tags = {
      app   = "yumdrake"
      env   = var.environment
      owner = var.owner
    }
  }
}
