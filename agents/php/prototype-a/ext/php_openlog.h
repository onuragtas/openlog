/* openlog PHP agent spike (option A). SPDX-License-Identifier: Apache-2.0 */
#ifndef PHP_OPENLOG_H
#define PHP_OPENLOG_H

extern zend_module_entry openlog_module_entry;
#define phpext_openlog_ptr &openlog_module_entry

#define PHP_OPENLOG_VERSION "0.0.1-spike"

#if defined(ZTS) && defined(COMPILE_DL_OPENLOG)
ZEND_TSRMLS_CACHE_EXTERN()
#endif

#endif
