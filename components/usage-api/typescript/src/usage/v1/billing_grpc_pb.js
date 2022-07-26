/**
 * Copyright (c) 2022 Gitpod GmbH. All rights reserved.
 * Licensed under the GNU Affero General Public License (AGPL).
 * See License-AGPL.txt in the project root for license information.
 */

// GENERATED CODE -- DO NOT EDIT!

'use strict';
var grpc = require('@grpc/grpc-js');
var usage_v1_billing_pb = require('../../usage/v1/billing_pb.js');
var google_protobuf_timestamp_pb = require('google-protobuf/google/protobuf/timestamp_pb.js');
var usage_v1_usage_pb = require('../../usage/v1/usage_pb.js');

function serialize_usage_v1_UpdateInvoicesRequest(arg) {
  if (!(arg instanceof usage_v1_billing_pb.UpdateInvoicesRequest)) {
    throw new Error('Expected argument of type usage.v1.UpdateInvoicesRequest');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_usage_v1_UpdateInvoicesRequest(buffer_arg) {
  return usage_v1_billing_pb.UpdateInvoicesRequest.deserializeBinary(new Uint8Array(buffer_arg));
}

function serialize_usage_v1_UpdateInvoicesResponse(arg) {
  if (!(arg instanceof usage_v1_billing_pb.UpdateInvoicesResponse)) {
    throw new Error('Expected argument of type usage.v1.UpdateInvoicesResponse');
  }
  return Buffer.from(arg.serializeBinary());
}

function deserialize_usage_v1_UpdateInvoicesResponse(buffer_arg) {
  return usage_v1_billing_pb.UpdateInvoicesResponse.deserializeBinary(new Uint8Array(buffer_arg));
}


var BillingServiceService = exports.BillingServiceService = {
  updateInvoices: {
    path: '/usage.v1.BillingService/UpdateInvoices',
    requestStream: false,
    responseStream: false,
    requestType: usage_v1_billing_pb.UpdateInvoicesRequest,
    responseType: usage_v1_billing_pb.UpdateInvoicesResponse,
    requestSerialize: serialize_usage_v1_UpdateInvoicesRequest,
    requestDeserialize: deserialize_usage_v1_UpdateInvoicesRequest,
    responseSerialize: serialize_usage_v1_UpdateInvoicesResponse,
    responseDeserialize: deserialize_usage_v1_UpdateInvoicesResponse,
  },
};

exports.BillingServiceClient = grpc.makeGenericClientConstructor(BillingServiceService);