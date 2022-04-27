// Copyright 2019 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

#pragma once

#include "api/envoy/v11/http/common/base.pb.h"
#include "envoy/common/pure.h"
#include "envoy/tracing/http_tracer.h"
#include "envoy/upstream/cluster_manager.h"
#include "google/protobuf/stubs/status.h"

namespace espv2 {
namespace envoy {
namespace http_filters {
namespace service_control {

class HttpCall {
 public:
  using DoneFunc =
      std::function<void(const ::google::protobuf::util::Status& status,
                         const std::string& response_body)>;

  virtual ~HttpCall() {}
  /*
   * Cancel any in-flight request.
   */
  virtual void cancel() PURE;

  virtual void call() PURE;
};

class HttpCallFactory
    : public Envoy::Logger::Loggable<Envoy::Logger::Id::filter> {
 public:
  virtual HttpCall* createHttpCall(const Envoy::Protobuf::Message& body,
                                   Envoy::Tracing::Span& parent_span,
                                   HttpCall::DoneFunc on_done) PURE;

  virtual ~HttpCallFactory(){};
};

class HttpCallFactoryImpl : public HttpCallFactory {
 public:
  HttpCallFactoryImpl(
      Envoy::Upstream::ClusterManager& cm, Envoy::Event::Dispatcher& dispatcher,
      const ::espv2::api::envoy::v11::http::common::HttpUri& uri,
      const std::string& suffix_url,
      std::function<const std::string&()> token_fn, uint32_t timeout_ms,
      uint32_t retries, Envoy::TimeSource& time_source,
      const std::string& trace_operation_name);

  HttpCall* createHttpCall(const Envoy::Protobuf::Message& body,
                           Envoy::Tracing::Span& parent_span,
                           HttpCall::DoneFunc on_done);

  ~HttpCallFactoryImpl();

 private:
  // all active calls generated by this factory
  absl::flat_hash_set<HttpCall*> active_calls_;

  // envoy upstream
  Envoy::Upstream::ClusterManager& cm_;
  Envoy::Event::Dispatcher& dispatcher_;

  // call uri address
  const ::espv2::api::envoy::v11::http::common::HttpUri uri_;
  const std::string suffix_url_;

  // token getter
  std::function<const std::string&()> token_fn_;

  // call setting
  uint32_t timeout_ms_;
  uint32_t retries_;

  // whether the factory is being destructed
  bool destruct_mode_;

  // tracing related
  Envoy::TimeSource& time_source_;
  const std::string trace_operation_name_;
};

}  // namespace service_control
}  // namespace http_filters
}  // namespace envoy
}  // namespace espv2
