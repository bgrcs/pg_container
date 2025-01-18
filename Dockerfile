FROM postgres as builder

ARG DB_NAME
ENV DB_NAME=${DB_NAME}
ENV PGDATA=/data

USER root
RUN mkdir -p ${PGDATA} && \
    chown -R postgres:postgres ${PGDATA} && \
    chmod -R 700 ${PGDATA}

USER postgres

COPY dump.sql /tmp/dump.sql

RUN initdb --pgdata=${PGDATA} && \
    pg_ctl -D ${PGDATA} -o "-c listen_addresses=''" -w start && \
    psql -U postgres -c "CREATE DATABASE ${DB_NAME};" && \
    psql -U postgres -d ${DB_NAME} -f /tmp/dump.sql && \
    psql -U postgres -c "ALTER USER postgres WITH PASSWORD 'postgres';" && \
    pg_ctl -D ${PGDATA} -m fast -w stop

FROM postgres

ENV PGDATA=/data

USER root
RUN mkdir -p ${PGDATA} && \
    chown -R postgres:postgres ${PGDATA} && \
    chmod -R 777 ${PGDATA}

COPY --from=builder ${PGDATA}/ ${PGDATA}/

COPY --from=builder /tmp/dump.sql dump.sql

RUN echo "listen_addresses = '*'" >> ${PGDATA}/postgresql.conf
RUN echo "host all all 0.0.0.0/0 md5" >> ${PGDATA}/pg_hba.conf

EXPOSE 5432

USER postgres

CMD ["postgres", "-c", "config_file=/data/postgresql.conf"]
