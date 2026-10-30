# Roostack (SPEC.md §8.1, "MySQL and MariaDB"). The image's entrypoint
# sources this as root, once, when a MySQL or MariaDB container Roostack
# made first starts, after it has made the app's user and database. It
# lets that user make and drop the schemas named <database>_<anything>, so
# Roostack can make the schemas a package asks for, now or when an update
# adds one, as the app's user: root's random password isn't kept.
roostack_user=${MARIADB_USER:-$MYSQL_USER}
roostack_db=${MARIADB_DATABASE:-$MYSQL_DATABASE}
if [ -n "$roostack_user" ] && [ -n "$roostack_db" ]; then
	mysql_note "Roostack: letting $roostack_user make schemas named ${roostack_db}_..."
	# In a grant's database name, _ matches any character and \_ only _.
	docker_process_sql --database=mysql <<<"GRANT ALL ON \`${roostack_db//_/\\_}\\_%\`.* TO '$roostack_user'@'%';"
fi
unset roostack_user roostack_db
