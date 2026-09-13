dnl openlog PHP agent spike (option A) - Apache-2.0
PHP_ARG_ENABLE([openlog],
  [whether to enable the openlog agent spike extension],
  [AS_HELP_STRING([--enable-openlog], [Enable openlog agent spike extension])],
  [yes])

if test "$PHP_OPENLOG" != "no"; then
  PHP_NEW_EXTENSION(openlog, openlog.c, $ext_shared,, -DZEND_ENABLE_STATIC_TSRMLS_CACHE=1 -Wall -Wextra -Wno-unused-parameter)
  PHP_ADD_EXTENSION_DEP(openlog, pdo)
fi
