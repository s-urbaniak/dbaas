# DBaaS sample provider Makefile
# Ref: dbaas.md for full topology description.

SHELL := /usr/bin/env bash
MAKEFLAGS += --no-builtin-rules
.SUFFIXES:

MK_DIR := mk

include $(MK_DIR)/common.mk

include $(MK_DIR)/crds.mk
include $(MK_DIR)/ako.mk
include $(MK_DIR)/bootstrap.mk
include $(MK_DIR)/capi.mk
include $(MK_DIR)/kcp.mk
include $(MK_DIR)/kro.mk
include $(MK_DIR)/headlamp.mk
include $(MK_DIR)/sync.mk
include $(MK_DIR)/images.mk
include $(MK_DIR)/pipeline.mk
