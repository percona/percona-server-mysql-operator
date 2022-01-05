.. _operator-configmaps:

Changing MySQL Options
======================

You may require a configuration change for your application. MySQL
allows the option to configure the database with a configuration file.
You can pass options from the
`my.cnf <https://dev.mysql.com/doc/refman/8.0/en/option-files.html>`__
configuration file to be included in the MySQL configuration in one of the
following ways:

* edit the ``deploy/cr.yaml`` file,
* use a ConfigMap,
* use a Secret object.

.. _operator-configmaps-cr:

Edit the ``deploy/cr.yaml`` file
---------------------------------

You can add options from the
`my.cnf <https://dev.mysql.com/doc/refman/8.0/en/option-files.html>`__
configuration file by editing the configuration section of the
``deploy/cr.yaml``. Here is an example:

.. code:: yaml

   spec:
     secretsName: cluster1-secrets
     pxc:
       ...
         configuration: |
           max_connections=250

See the `Custom Resource options, MySQL section <operator.html#operator-mysql-section>`_
for more details.

.. _operator-configmaps-auto:

Auto-tuning MySQL options
--------------------------

Few configuration options for MySQL can be calculated and set by the Operator
automatically based on the available Pod resources (memory and CPU) **if
these options are not specified by user** (either in CR.yaml or in ConfigMap).

Options which can be set automatically are the following ones:

* ``innodb_buffer_pool_size``
* ``max_connections``

If Percona Server for MySQL Pod limits are defined, then limits values are used to
calculate these options. If Percona Server for MySQL Pod limits are not defined,
Operator looks for Percona Server for MySQL Pod requests as the basis for
calculations. if neither Percona Server for MySQL Pod limits nor Server for
MySQL Pod requests are defined, auto-tuning is not done.
