FROM mysql:5.7

COPY ./*.sql /docker-entrypoint-initdb.d/
COPY ./*.cnf /etc/mysql/conf.d/

CMD ["--character-set-server=utf8mb4"]