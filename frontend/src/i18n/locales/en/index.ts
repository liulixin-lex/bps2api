import priorityScheduling from './priorityScheduling'
import qualityOps from './qualityOps'
import accountOps from './accountOps'
import tokenGuard from './tokenGuard'
import landing from './landing'
import common from './common'
import dashboard from './dashboard'
import channelMonitorV2 from './channelMonitorV2'
import batchImage from './batchImage'
import admin from './admin'
import misc from './misc'

import requestTiming from './requestTiming'

import autoConfig from './autoConfig'

export default {
  autoConfig,
  priorityScheduling,
  qualityOps,
  accountOps,
  tokenGuard,
  requestTiming,
  ...landing,
  ...common,
  ...dashboard,
  ...channelMonitorV2,
  ...batchImage,
  admin,
  ...misc,
}
